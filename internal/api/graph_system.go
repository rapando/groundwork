package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rapando/groundwork/internal/ansible"
	"github.com/rapando/groundwork/internal/graph"
	"github.com/rapando/groundwork/internal/proc"
	"github.com/rapando/groundwork/internal/runner"
	"github.com/rapando/groundwork/internal/terraform"
	"github.com/rapando/groundwork/internal/workspace"
)

// stateTTL: how long a pulled state is reused. Pulling a remote state takes
// seconds, and the view is refreshed with ?refresh=1 after an apply.
const stateTTL = time.Minute

type stateEntry struct {
	at    time.Time
	state *graph.State
	err   error
}

var (
	stateCache sync.Map // dir|workspace → stateEntry
	stateLoads sync.Map // dir|workspace → *sync.Mutex
)

// terraformState pulls the stack's state and reduces it at once: the raw
// state (which can hold secrets) is never kept, logged or written. It also
// returns when that state was pulled (it is cached for a minute).
func (a *API) terraformState(ctx context.Context, t runner.Target, refresh bool) (*graph.State, time.Time, error) {
	dir := filepath.Join(a.Root, filepath.FromSlash(t.Root))
	if _, err := os.Stat(filepath.Join(dir, ".terraform")); err != nil {
		return nil, time.Time{}, errors.New("the root isn't initialised (run init), so its state can't be read")
	}
	key := dir + "|" + t.Workspace
	if e, ok := stateCache.Load(key); ok && !refresh && time.Since(e.(stateEntry).at) < stateTTL {
		return e.(stateEntry).state, e.(stateEntry).at, e.(stateEntry).err
	}
	mu, _ := stateLoads.LoadOrStore(key, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	if e, ok := stateCache.Load(key); ok && time.Since(e.(stateEntry).at) < time.Second { // pulled while we waited
		return e.(stateEntry).state, e.(stateEntry).at, e.(stateEntry).err
	}
	s, err := a.pullState(ctx, t, dir)
	now := time.Now()
	stateCache.Store(key, stateEntry{now, s, err})
	return s, now, err
}

func (a *API) pullState(ctx context.Context, t runner.Target, dir string) (*graph.State, error) {
	bin, err := exec.LookPath(t.Binary)
	if err != nil {
		return nil, fmt.Errorf("%s isn't installed", t.Binary)
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, terraform.StateSerialArgs(t.Binary, dir)[1:]...)
	proc.GroupContext(cmd)
	cmd.Env = append(os.Environ(), "TF_IN_AUTOMATION=1", "NO_COLOR=1", "CHECKPOINT_DISABLE=1")
	if t.Workspace != "" {
		cmd.Env = append(cmd.Env, "TF_WORKSPACE="+t.Workspace)
	}
	out, err := cmd.Output()
	if err != nil {
		// stderr can name backends and buckets but not state values
		var ee *exec.ExitError
		if errors.As(err, &ee) && len(ee.Stderr) > 0 {
			return nil, fmt.Errorf("terraform state pull failed: %s", firstLine(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("terraform state pull failed: %v", err)
	}
	if len(strings.TrimSpace(string(out))) == 0 {
		return nil, errors.New("there is no state yet (nothing has been applied)")
	}
	return graph.ParseState(out)
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "Error:")); l != "" && !strings.HasPrefix(l, "╷") && !strings.HasPrefix(l, "│") {
			return l
		}
	}
	return strings.TrimSpace(s)
}

type systemScope struct {
	Project string `json:"project"`
	Env     string `json:"env"`
	Hosts   int    `json:"hosts"`
	Error   string `json:"error,omitempty"`
}

// graphSystem: the architecture view with the Ansible hosts of the same
// environment placed on the Terraform instances they run on.
func (a *API) graphSystem(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	t, ok := a.graphTarget(w, q.Get("root"), q.Get("env"))
	if !ok {
		return
	}
	g := graph.Build(graph.Options{RepoRoot: a.Root, RootDir: filepath.Join(a.Root, filepath.FromSlash(t.Root)), VarVals: a.varValues(t)})
	actions, drifted, _ := a.overlays(t)
	g.Overlay(actions, drifted)
	a.markErrors(g)
	rules := graph.LoadRules()
	arch := graph.BuildArchitecture(g, rules)

	hosts, scopes := a.systemHosts(r.Context(), t.Env)
	st, pulledAt, err := a.terraformState(r.Context(), t, q.Get("refresh") == "1")
	stateNote := ""
	if err != nil {
		stateNote = err.Error()
	}
	sys := graph.BuildSystem(arch, st, hosts, rules, "state unavailable: "+stateNote)
	out := map[string]any{"system": sys, "state_error": stateNote, "scopes": scopes, "target": t}
	if err == nil && !pulledAt.IsZero() {
		out["state_pulled_at"] = pulledAt.UTC().Format(time.RFC3339)
	}
	writeJSON(w, 200, out)
}

// systemHosts collects the hosts of every Ansible project's inventory for
// env. A project without `inventories:` has one inventory for every
// environment ("default"), so it pairs with any stack.
func (a *API) systemHosts(ctx context.Context, env string) ([]graph.SystemHost, []systemScope) {
	cfg, _ := a.config()
	var hosts []graph.SystemHost
	scopes := []systemScope{}
	var playbooks map[string][]string // project → playbooks relative to it
	for _, s := range scopesFor(cfg, a.Root) {
		if s.Env != env && s.Env != "default" {
			continue
		}
		sc := systemScope{Project: s.Project, Env: s.Env}
		inv, err := a.loadInventory(ctx, s)
		if err != nil {
			sc.Error = err.Error()
			scopes = append(scopes, sc)
			continue
		}
		if playbooks == nil {
			playbooks = a.projectPlaybooks()
		}
		dir := filepath.Join(a.Root, filepath.FromSlash(s.Project))
		roles := hostRoles(dir, inv, ansible.Plays(dir, playbooks[s.Project]))
		facts, _ := a.Store.AllFacts(s.Project + "#" + s.Env)
		for _, h := range inv.Hosts {
			sh := graph.SystemHost{Project: s.Project, Env: s.Env, Name: h, Address: inv.Address(h), Roles: []graph.RoleUse{}, Groups: []graph.HostGroup{}}
			for _, g := range inv.DirectGroups(h) {
				sh.Groups = append(sh.Groups, graph.HostGroup{Name: g, Path: inv.ParentPath(g)})
			}
			var refs []ansible.RoleRef
			for _, ru := range roles[h] {
				sh.Roles = append(sh.Roles, graph.RoleUse{Name: ru.name, Playbooks: ru.playbooks})
				refs = append(refs, ansible.RoleRef{Name: ru.name, Playbook: ru.playbooks[0]})
			}
			for _, p := range ansible.DeclaredPorts(dir, inv, h, refs) {
				sh.Declared = append(sh.Declared, graph.DeclaredPort{Port: p.Port, Proto: p.Proto, Var: p.Var, Role: p.Role})
			}
			if f, ok := facts[h]; ok {
				applyFacts(&sh, f.JSON)
			}
			hosts = append(hosts, sh)
		}
		sc.Hosts = len(inv.Hosts)
		scopes = append(scopes, sc)
	}
	return hosts, scopes
}

// projectPlaybooks lists each Ansible project's playbooks, relative to it.
func (a *API) projectPlaybooks() map[string][]string {
	out := map[string][]string{}
	rep, err := workspace.Detect(a.Root, a.Ignore())
	if err != nil {
		return out
	}
	for _, p := range rep.Ansible {
		for _, pb := range p.Playbooks {
			out[p.Path] = append(out[p.Path], strings.TrimPrefix(pb, p.Path+"/"))
		}
	}
	return out
}

type roleUse struct {
	name      string
	playbooks []string
}

// hostRoles: per host, the roles of every play that targets it (with their
// dependencies), in first-seen order.
func hostRoles(dir string, inv *ansible.Inventory, plays []ansible.Play) map[string][]roleUse {
	out := map[string][]roleUse{}
	idx := map[string]map[string]int{} // host → role → position in out[host]
	for _, p := range plays {
		hosts, ok := inv.Match(p.Hosts)
		if !ok {
			continue
		}
		roles := ansible.RoleClosure(dir, p.Playbook, p.Roles)
		for _, h := range hosts {
			if idx[h] == nil {
				idx[h] = map[string]int{}
			}
			for _, r := range roles {
				if i, seen := idx[h][r]; seen {
					if pbs := out[h][i].playbooks; pbs[len(pbs)-1] != p.Playbook {
						out[h][i].playbooks = append(pbs, p.Playbook)
					}
					continue
				}
				idx[h][r] = len(out[h])
				out[h] = append(out[h], roleUse{r, []string{p.Playbook}})
			}
		}
	}
	return out
}

// applyFacts takes the OS and the listening sockets from cached facts.
func applyFacts(sh *graph.SystemHost, js string) {
	var m map[string]any
	if json.Unmarshal([]byte(js), &m) != nil {
		return
	}
	if d, ok := m["ansible_distribution"].(string); ok {
		v, _ := m["ansible_distribution_version"].(string)
		sh.OS = strings.TrimSpace(d + " " + v)
	}
	at, _ := m["groundwork_listen_at"].(string)
	ts, err := time.Parse(time.RFC3339, at)
	if err != nil {
		return
	}
	sh.ListenAt = &ts
	for _, k := range []string{"tcp_listen", "udp_listen"} {
		l, _ := m[k].([]any)
		for _, x := range l {
			s, _ := x.(map[string]any)
			port, _ := s["port"].(float64)
			proto, _ := s["protocol"].(string)
			if proto == "" {
				proto = strings.TrimSuffix(k, "_listen")
			}
			addr, _ := s["address"].(string)
			name, _ := s["name"].(string)
			if port > 0 {
				sh.Listening = append(sh.Listening, graph.ListenPort{Port: int(port), Proto: strings.TrimRight(proto, "46"), Address: addr, Process: name})
			}
		}
	}
	sort.Slice(sh.Listening, func(i, j int) bool { return sh.Listening[i].Port < sh.Listening[j].Port })
}
