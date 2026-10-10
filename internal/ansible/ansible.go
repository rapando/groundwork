// Package ansible adapts the Ansible CLI: argv builders, the embedded callback
// plugin and its event stream, inventory parsing and variable provenance.
package ansible

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

//go:embed callback/groundwork.py
var callbackSource []byte

// InstallCallback writes the callback plugin into dir and returns the
// environment that enables it. Events arrive on file descriptor 3.
func InstallCallback(dir string) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "groundwork.py"), callbackSource, 0o644); err != nil {
		return nil, err
	}
	return []string{
		"ANSIBLE_CALLBACK_PLUGINS=" + dir,
		"ANSIBLE_CALLBACKS_ENABLED=groundwork",
		"ANSIBLE_LOAD_CALLBACK_PLUGINS=1", // ad-hoc `ansible` ignores callbacks without this
		"GROUNDWORK_EVENTS_FD=3",
	}, nil
}

// ---- events ----

type HostStats struct {
	Ok          int `json:"ok"`
	Changed     int `json:"changed"`
	Failures    int `json:"failures"`
	Unreachable int `json:"unreachable"`
	Skipped     int `json:"skipped"`
	Rescued     int `json:"rescued"`
	Ignored     int `json:"ignored"`
}

// HostResult is one task's outcome on one host.
type HostResult struct {
	Play     string         `json:"play"`
	Task     string         `json:"task"`
	Action   string         `json:"action"`
	Host     string         `json:"host"`
	Status   string         `json:"status"` // ok | changed | failed | skipped | unreachable
	Changed  bool           `json:"changed"`
	Ignored  bool           `json:"ignored,omitempty"`
	Msg      string         `json:"msg,omitempty"`
	Diff     string         `json:"diff,omitempty"`
	Duration float64        `json:"duration,omitempty"`
	Facts    map[string]any `json:"facts,omitempty"`
}

type Event struct {
	Type   string               `json:"type"` // playbook_start | play_start | task_start | host_result | stats
	Play   string               `json:"play,omitempty"`
	Task   string               `json:"task,omitempty"`
	Result *HostResult          `json:"result,omitempty"`
	Stats  map[string]HostStats `json:"stats,omitempty"`
}

// ParseEvent decodes one NDJSON line from the callback.
func ParseEvent(b []byte) (Event, bool) {
	var raw struct {
		Type  string               `json:"type"`
		Play  string               `json:"play"`
		Task  string               `json:"task"`
		Hosts map[string]HostStats `json:"hosts"`
	}
	if json.Unmarshal(b, &raw) != nil || raw.Type == "" {
		return Event{}, false
	}
	e := Event{Type: raw.Type, Play: raw.Play, Task: raw.Task}
	switch raw.Type {
	case "host_result":
		var hr HostResult
		if json.Unmarshal(b, &hr) != nil {
			return Event{}, false
		}
		e.Result = &hr
	case "stats":
		e.Stats = raw.Hosts
	}
	return e, true
}

// ---- argv ----

var (
	patternRe = regexp.MustCompile(`^[A-Za-z0-9_.:*!&,\[\]~@-]+$`)
	moduleRe  = regexp.MustCompile(`^[a-z0-9_]+(\.[a-z0-9_]+)*$`)
)

// ValidPattern accepts host patterns/limits/tags; never a leading "-" (it
// would be read as a flag) and never whitespace.
func ValidPattern(s string) bool {
	return s != "" && !strings.HasPrefix(s, "-") && patternRe.MatchString(s)
}

func ValidModule(s string) bool { return moduleRe.MatchString(s) }

type PlaybookOpts struct {
	Inventory    string // path relative to the project, "" = ansible.cfg default
	Playbook     string // relative to the project
	Check        bool
	Limit        string
	Tags         string
	SkipTags     string
	VaultPwdFile string
	ExtraVars    []string // key=value, from tests/config only
}

func invArgs(inv string) []string {
	if inv == "" {
		return nil
	}
	return []string{"-i", inv}
}

// PlaybookArgs builds an ansible-playbook argv. Values use --flag=value form
// so a value can never be parsed as another flag.
func PlaybookArgs(o PlaybookOpts) []string {
	a := append([]string{"ansible-playbook"}, invArgs(o.Inventory)...)
	a = append(a, "--diff")
	if o.Check {
		a = append(a, "--check")
	}
	if o.Limit != "" {
		a = append(a, "--limit="+o.Limit)
	}
	if o.Tags != "" {
		a = append(a, "--tags="+o.Tags)
	}
	if o.SkipTags != "" {
		a = append(a, "--skip-tags="+o.SkipTags)
	}
	if o.VaultPwdFile != "" {
		a = append(a, "--vault-password-file="+o.VaultPwdFile)
	}
	for _, ev := range o.ExtraVars {
		a = append(a, "--extra-vars="+ev)
	}
	return append(a, "--", o.Playbook)
}

func SyntaxArgs(o PlaybookOpts) []string {
	a := append([]string{"ansible-playbook"}, invArgs(o.Inventory)...)
	if o.VaultPwdFile != "" {
		a = append(a, "--vault-password-file="+o.VaultPwdFile)
	}
	return append(a, "--syntax-check", "--", o.Playbook)
}

// AdhocArgs builds `ansible <pattern> -m <module> [-a <args>]`.
func AdhocArgs(inventory, pattern, module, args string, check bool, vault string) []string {
	a := append([]string{"ansible"}, invArgs(inventory)...)
	a = append(a, "-m", module)
	if args != "" {
		a = append(a, "-a", args)
	}
	if check {
		a = append(a, "--check")
	}
	if vault != "" {
		a = append(a, "--vault-password-file="+vault)
	}
	return append(a, "--", pattern)
}

// InventoryListArgs lists an inventory with its vars, so vault-encrypted
// group_vars need the vault password file ("" = none).
func InventoryListArgs(inventory, playbookDir, vault string) []string {
	return append(append([]string{"ansible-inventory"}, invArgs(inventory)...), append(vaultArgs(vault), "--list", "--playbook-dir="+playbookDir)...)
}

func InventoryHostArgs(inventory, playbookDir, host, vault string) []string {
	return append(append([]string{"ansible-inventory"}, invArgs(inventory)...), append(vaultArgs(vault), "--host="+host, "--playbook-dir="+playbookDir)...)
}

func vaultArgs(vault string) []string {
	if vault == "" {
		return nil
	}
	return []string{"--vault-password-file=" + vault}
}

// ReadOnlyModules never change a host, so ad-hoc runs of them need no approval.
var ReadOnlyModules = map[string]bool{
	"ping": true, "ansible.builtin.ping": true,
	"setup": true, "ansible.builtin.setup": true, "gather_facts": true, "ansible.builtin.gather_facts": true,
	"stat": true, "ansible.builtin.stat": true,
	"debug": true, "ansible.builtin.debug": true,
	"package_facts": true, "ansible.builtin.package_facts": true,
	"service_facts": true, "ansible.builtin.service_facts": true,
}

// ---- inventory ----

type Group struct {
	Name     string   `json:"name"`
	Hosts    []string `json:"hosts"`    // direct members
	Children []string `json:"children"` // child groups
	Depth    int      `json:"depth"`    // all = 0
	Total    int      `json:"total"`    // hosts including children
}

type Inventory struct {
	Groups   map[string]*Group         `json:"groups"`
	HostVars map[string]map[string]any `json:"-"`
	Hosts    []string                  `json:"hosts"`
}

// ParseInventoryList reads `ansible-inventory --list` output.
func ParseInventoryList(b []byte) (*Inventory, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("parse ansible-inventory output: %w", err)
	}
	inv := &Inventory{Groups: map[string]*Group{}, HostVars: map[string]map[string]any{}}
	for name, msg := range raw {
		if name == "_meta" {
			var meta struct {
				HostVars map[string]map[string]any `json:"hostvars"`
			}
			_ = json.Unmarshal(msg, &meta)
			inv.HostVars = meta.HostVars
			continue
		}
		var g struct {
			Hosts    []string `json:"hosts"`
			Children []string `json:"children"`
		}
		_ = json.Unmarshal(msg, &g)
		inv.Groups[name] = &Group{Name: name, Hosts: g.Hosts, Children: g.Children}
	}
	if inv.Groups["all"] == nil {
		inv.Groups["all"] = &Group{Name: "all"}
	}
	// depth by walking from "all"; a group reachable at several depths keeps the deepest
	var walk func(name string, d int, seen map[string]bool)
	walk = func(name string, d int, seen map[string]bool) {
		g := inv.Groups[name]
		if g == nil || seen[name] {
			return
		}
		if d > g.Depth {
			g.Depth = d
		}
		seen[name] = true
		for _, c := range g.Children {
			walk(c, d+1, seen)
		}
		delete(seen, name)
	}
	walk("all", 0, map[string]bool{})
	hosts := map[string]bool{}
	for h := range inv.HostVars {
		hosts[h] = true
	}
	for _, g := range inv.Groups {
		for _, h := range g.Hosts {
			hosts[h] = true
		}
	}
	for h := range hosts {
		inv.Hosts = append(inv.Hosts, h)
	}
	sort.Strings(inv.Hosts)
	for _, g := range inv.Groups {
		g.Total = len(inv.MembersOf(g.Name))
	}
	return inv, nil
}

// MembersOf returns every host in a group, including its children's hosts.
func (inv *Inventory) MembersOf(group string) []string {
	out := map[string]bool{}
	var walk func(string, map[string]bool)
	walk = func(name string, seen map[string]bool) {
		g := inv.Groups[name]
		if g == nil || seen[name] {
			return
		}
		seen[name] = true
		for _, h := range g.Hosts {
			out[h] = true
		}
		for _, c := range g.Children {
			walk(c, seen)
		}
	}
	if group == "all" {
		for _, h := range inv.Hosts {
			out[h] = true
		}
	} else {
		walk(group, map[string]bool{})
	}
	list := make([]string, 0, len(out))
	for h := range out {
		list = append(list, h)
	}
	sort.Strings(list)
	return list
}

// GroupsOf returns the groups a host belongs to (directly or through a child),
// ordered as Ansible merges them: by depth, then name. "all" comes first.
func (inv *Inventory) GroupsOf(host string) []*Group {
	var out []*Group
	for _, g := range inv.Groups {
		if g.Name == "all" {
			out = append(out, g)
			continue
		}
		for _, h := range inv.MembersOf(g.Name) {
			if h == host {
				out = append(out, g)
				break
			}
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Depth != out[j].Depth {
			return out[i].Depth < out[j].Depth
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// Address is how a host is reached.
func (inv *Inventory) Address(host string) string {
	if v, ok := inv.HostVars[host]["ansible_host"].(string); ok && v != "" {
		return v
	}
	if c, _ := inv.HostVars[host]["ansible_connection"].(string); c == "local" {
		return "local"
	}
	return host
}

// Query runs a read-only ansible command (ansible-inventory) in the project
// directory and returns its stdout.
func Query(ctx context.Context, projectDir, ansibleHome string, argv []string) ([]byte, error) {
	bin, err := exec.LookPath(argv[0])
	if err != nil {
		return nil, fmt.Errorf("%s not found on PATH (pipx install ansible-core)", argv[0])
	}
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, argv[1:]...)
	cmd.Dir = projectDir
	_ = os.MkdirAll(ansibleHome, 0o755)
	cmd.Env = append(os.Environ(), "ANSIBLE_HOME="+ansibleHome, "ANSIBLE_NOCOLOR=1", "ANSIBLE_FORCE_COLOR=0")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		for _, l := range strings.Split(msg, "\n") {
			if strings.Contains(l, "ERROR") {
				msg = strings.TrimSpace(l)
				break
			}
		}
		return nil, fmt.Errorf("%s: %s", argv[0], firstNonEmpty(msg, err.Error()))
	}
	return out, nil
}

func firstNonEmpty(a ...string) string {
	for _, s := range a {
		if s != "" {
			return s
		}
	}
	return ""
}
