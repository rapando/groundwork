package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/rapando/groundwork/internal/ansible"
	"github.com/rapando/groundwork/internal/config"
	"github.com/rapando/groundwork/internal/runner"
	"github.com/rapando/groundwork/internal/store"
	"github.com/rapando/groundwork/internal/workspace"
)

type invScope struct {
	Project   string `json:"project"`
	Env       string `json:"env"`
	Inventory string `json:"inventory"` // relative to the project ("" = ansible.cfg default)
}

func scopesFor(cfg *config.Config) []invScope {
	var out []invScope
	if cfg == nil {
		return out
	}
	for _, p := range cfg.Ansible.Projects {
		if len(p.Inventories) == 0 {
			out = append(out, invScope{Project: path.Clean(p.Path), Env: "default"})
			continue
		}
		envs := make([]string, 0, len(p.Inventories))
		for e := range p.Inventories {
			envs = append(envs, e)
		}
		sort.Slice(envs, func(i, j int) bool {
			return envRank(envs[i]) < envRank(envs[j]) || envRank(envs[i]) == envRank(envs[j]) && envs[i] < envs[j]
		})
		for _, e := range envs {
			out = append(out, invScope{Project: path.Clean(p.Path), Env: e, Inventory: p.Inventories[e]})
		}
	}
	return out
}

type invCacheEntry struct {
	fp  string
	inv *ansible.Inventory
}

var invCache sync.Map // scope → invCacheEntry

// invLoads serialises cold loads per scope, so concurrent requests share one
// ansible-inventory process instead of each starting their own.
var invLoads sync.Map // scope → *sync.Mutex

// projectFingerprint is cheap (sizes and mtimes) and decides when to re-run ansible-inventory.
func projectFingerprint(dir string) string {
	h := sha256.New()
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == ".groundwork" || d.Name() == ".ansible") {
			return filepath.SkipDir
		}
		if info, err := d.Info(); err == nil && !d.IsDir() {
			fmt.Fprintf(h, "%s %d %d\n", p, info.Size(), info.ModTime().UnixNano())
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))
}

func (a *API) ansibleHome() string { return filepath.Join(a.Root, ".groundwork", "ansible-home") }

func (a *API) loadInventory(ctx context.Context, s invScope) (*ansible.Inventory, error) {
	dir := filepath.Join(a.Root, filepath.FromSlash(s.Project))
	key := s.Project + "#" + s.Env
	fp := projectFingerprint(dir)
	if e, ok := invCache.Load(key); ok && e.(invCacheEntry).fp == fp {
		return e.(invCacheEntry).inv, nil
	}
	mu, _ := invLoads.LoadOrStore(key, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	if e, ok := invCache.Load(key); ok && e.(invCacheEntry).fp == fp { // loaded while we waited
		return e.(invCacheEntry).inv, nil
	}
	out, err := ansible.Query(ctx, dir, a.ansibleHome(), ansible.InventoryListArgs(s.Inventory, dir))
	if err != nil {
		return nil, err
	}
	inv, err := ansible.ParseInventoryList(out)
	if err != nil {
		return nil, err
	}
	invCache.Store(key, invCacheEntry{fp, inv})
	return inv, nil
}

func (a *API) scopeFromQuery(r *http.Request) (invScope, []invScope, error) {
	cfg, _ := a.config()
	all := scopesFor(cfg)
	if len(all) == 0 {
		return invScope{}, all, fmt.Errorf("no Ansible projects are configured")
	}
	q := r.URL.Query()
	if q.Get("project") == "" && q.Get("env") == "" {
		return all[0], all, nil
	}
	for _, s := range all {
		if (q.Get("project") == "" || s.Project == path.Clean(q.Get("project"))) && s.Env == q.Get("env") {
			return s, all, nil
		}
	}
	return invScope{}, all, fmt.Errorf("unknown project/environment")
}

// inventoryFile is the absolute inventory path of a scope.
func (a *API) inventoryFile(s invScope) string {
	dir := filepath.Join(a.Root, filepath.FromSlash(s.Project))
	inv := s.Inventory
	if inv == "" {
		inv = ansible.DefaultInventory(filepath.Join(dir, "ansible.cfg"))
	}
	if inv == "" {
		return ""
	}
	if filepath.IsAbs(inv) {
		return inv
	}
	return filepath.Join(dir, filepath.FromSlash(inv))
}

type lastPlay struct {
	RunID       int64  `json:"run_id"`
	Kind        string `json:"kind"`
	Status      string `json:"status"`
	Phase       string `json:"phase"` // run | check
	Ok          int    `json:"ok"`
	Changed     int    `json:"changed"`
	Failures    int    `json:"failures"`
	Unreachable int    `json:"unreachable"`
}

// lastPlays returns, per host, the outcome of the newest playbook run in a scope.
func (a *API) lastPlays(s invScope) map[string]lastPlay {
	out := map[string]lastPlay{}
	runs, _ := a.Store.ListRuns(store.RunFilter{Limit: 300})
	for _, run := range runs { // newest first
		if run.Kind != runner.KindAnsPlaybook && run.Kind != runner.KindAnsCheck {
			continue
		}
		var t runner.Target
		_ = json.Unmarshal(run.Target, &t)
		if t.Project != s.Project || t.Env != s.Env {
			continue
		}
		var sum runner.Summary
		_ = json.Unmarshal(run.Summary, &sum)
		if sum.Ansible == nil {
			continue
		}
		stats, phase := sum.Ansible.Run, "run"
		if stats == nil {
			stats, phase = sum.Ansible.Check, "check"
		}
		for h, st := range stats {
			if _, seen := out[h]; !seen {
				out[h] = lastPlay{RunID: run.ID, Kind: run.Kind, Status: run.Status, Phase: phase, Ok: st.Ok, Changed: st.Changed, Failures: st.Failures, Unreachable: st.Unreachable}
			}
		}
	}
	return out
}

func (a *API) getInventory(w http.ResponseWriter, r *http.Request) {
	s, all, err := a.scopeFromQuery(r)
	if err != nil {
		writeJSON(w, 200, map[string]any{"scopes": all, "error": err.Error()})
		return
	}
	inv, err := a.loadInventory(r.Context(), s)
	if err != nil {
		writeJSON(w, 200, map[string]any{"scopes": all, "scope": s, "error": err.Error()})
		return
	}
	scope := s.Project + "#" + s.Env
	status, _ := a.Store.HostStatuses(scope)
	facts, _ := a.Store.AllFacts(scope)
	plays := a.lastPlays(s)

	type hostRow struct {
		Name      string            `json:"name"`
		Address   string            `json:"address"`
		Groups    []string          `json:"groups"`
		OS        string            `json:"os,omitempty"`
		Reachable *store.HostStatus `json:"reachable,omitempty"`
		LastPlay  *lastPlay         `json:"last_play,omitempty"`
	}
	hosts := make([]hostRow, 0, len(inv.Hosts))
	down := map[string]int{}
	for _, h := range inv.Hosts {
		row := hostRow{Name: h, Address: inv.Address(h), Groups: []string{}}
		for _, g := range inv.GroupsOf(h) {
			if g.Name != "all" {
				row.Groups = append(row.Groups, g.Name)
			}
		}
		if f, ok := facts[h]; ok {
			var m map[string]any
			if json.Unmarshal([]byte(f.JSON), &m) == nil {
				row.OS = strings.TrimSpace(fmt.Sprint(m["ansible_distribution"]) + " " + fmt.Sprint(m["ansible_distribution_version"]))
			}
		}
		if st, ok := status[h]; ok {
			st := st
			row.Reachable = &st
			if !st.Reachable {
				for _, g := range row.Groups {
					down[g]++
				}
				down["all"]++
			}
		}
		if lp, ok := plays[h]; ok {
			lp := lp
			row.LastPlay = &lp
		}
		hosts = append(hosts, row)
	}
	type groupRow struct {
		*ansible.Group
		Down int `json:"down"`
	}
	groups := make([]groupRow, 0, len(inv.Groups))
	for _, g := range inv.Groups {
		if g.Name == "ungrouped" && g.Total == 0 {
			continue
		}
		groups = append(groups, groupRow{g, down[g.Name]})
	}
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Name == "all" || groups[j].Name == "all" {
			return groups[i].Name == "all"
		}
		return groups[i].Name < groups[j].Name
	})

	// files that shape this inventory (for quick editing)
	dir := filepath.Join(a.Root, filepath.FromSlash(s.Project))
	invFile := a.inventoryFile(s)
	var fileList []string
	rel := func(p string) string { r, _ := filepath.Rel(a.Root, p); return filepath.ToSlash(r) }
	if invFile != "" {
		if st, err := os.Stat(invFile); err == nil && !st.IsDir() {
			fileList = append(fileList, rel(invFile))
		}
	}
	for _, base := range []string{filepath.Dir(invFile), dir} {
		for _, kind := range []string{"group_vars", "host_vars"} {
			_ = filepath.WalkDir(filepath.Join(base, kind), func(p string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					fileList = append(fileList, rel(p))
				}
				return nil
			})
		}
	}
	fileList = uniqueSorted(fileList)

	var playbooks []string
	if rep, err := workspace.Detect(a.Root, a.Ignore()); err == nil {
		for _, p := range rep.Ansible {
			if p.Path == s.Project {
				for _, pb := range p.Playbooks {
					playbooks = append(playbooks, strings.TrimPrefix(pb, s.Project+"/"))
				}
			}
		}
	}
	cfg, _ := a.config()
	writeJSON(w, 200, map[string]any{
		"scopes": all, "scope": s, "hosts": hosts, "groups": groups, "files": fileList, "playbooks": playbooks,
		"approval_required": config.IsProdLike(s.Env), "source": rel(invFile), "configured": cfg != nil,
	})
}

func uniqueSorted(l []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, x := range l {
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	sort.Strings(out)
	return out
}

func (a *API) getInventoryHost(w http.ResponseWriter, r *http.Request) {
	s, _, err := a.scopeFromQuery(r)
	if err != nil {
		writeError(w, 400, "bad_request", err.Error(), nil)
		return
	}
	host := r.URL.Query().Get("host")
	inv, err := a.loadInventory(r.Context(), s)
	if err != nil {
		writeError(w, 502, "ansible_failed", err.Error(), nil)
		return
	}
	known := false
	for _, h := range inv.Hosts {
		known = known || h == host
	}
	if !known {
		writeError(w, 404, "not_found", "no such host in this inventory", nil)
		return
	}
	dir := filepath.Join(a.Root, filepath.FromSlash(s.Project))
	out, err := ansible.Query(r.Context(), dir, a.ansibleHome(), ansible.InventoryHostArgs(s.Inventory, dir, host))
	if err != nil {
		writeError(w, 502, "ansible_failed", err.Error(), nil)
		return
	}
	var actual map[string]any
	_ = json.Unmarshal(out, &actual)
	vars := ansible.Provenance(a.Root, dir, a.inventoryFile(s), inv, host, actual)

	scope := s.Project + "#" + s.Env
	resp := map[string]any{"host": host, "address": inv.Address(host), "vars": vars}
	var groups []string
	for _, g := range inv.GroupsOf(host) {
		groups = append(groups, g.Name)
	}
	resp["groups"] = groups
	if f, _ := a.Store.GetFacts(scope, host); f != nil {
		var m map[string]any
		_ = json.Unmarshal([]byte(f.JSON), &m)
		resp["facts"], resp["facts_gathered_at"] = m, f.GatheredAt
	}
	if st, _ := a.Store.HostStatuses(scope); st != nil {
		if hs, ok := st[host]; ok {
			resp["reachable"] = hs
		}
	}
	writeJSON(w, 200, resp)
}
