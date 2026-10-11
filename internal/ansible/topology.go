package ansible

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Play is one play of a playbook, reduced to whom it targets and the roles it applies.
type Play struct {
	Playbook string   // relative to the project
	Hosts    string   // host pattern
	Roles    []string // roles: entries and include_role/import_role tasks
}

// Plays reads the plays of each playbook (paths relative to projectDir).
// Files that don't parse are skipped: the checks report them.
func Plays(projectDir string, playbooks []string) []Play {
	var out []Play
	for _, pb := range playbooks {
		b, err := os.ReadFile(filepath.Join(projectDir, filepath.FromSlash(pb)))
		if err != nil {
			continue
		}
		var doc []map[string]any
		if yaml.Unmarshal(b, &doc) != nil {
			continue
		}
		for _, play := range doc {
			hosts := patternOf(play["hosts"])
			if hosts == "" {
				continue // import_playbook entries and the like
			}
			p := Play{Playbook: pb, Hosts: hosts}
			for _, r := range list(play["roles"]) {
				switch x := r.(type) {
				case string:
					p.Roles = append(p.Roles, x)
				case map[string]any:
					if n := firstString(x, "role", "name"); n != "" {
						p.Roles = append(p.Roles, n)
					}
				}
			}
			for _, k := range []string{"pre_tasks", "tasks", "post_tasks", "handlers"} {
				p.Roles = append(p.Roles, taskRoles(list(play[k]))...)
			}
			out = append(out, p)
		}
	}
	return out
}

func patternOf(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case []any:
		var parts []string
		for _, p := range x {
			if s, ok := p.(string); ok {
				parts = append(parts, strings.TrimSpace(s))
			}
		}
		return strings.Join(parts, ",")
	}
	return ""
}

var includeRole = map[string]bool{"include_role": true, "import_role": true, "ansible.builtin.include_role": true, "ansible.builtin.import_role": true}

// taskRoles finds roles pulled in by tasks, including inside blocks.
func taskRoles(tasks []any) []string {
	var out []string
	for _, t := range tasks {
		m, ok := t.(map[string]any)
		if !ok {
			continue
		}
		for k, v := range m {
			if includeRole[k] {
				if args, ok := v.(map[string]any); ok {
					if n := firstString(args, "name"); n != "" && !strings.Contains(n, "{{") {
						out = append(out, n)
					}
				}
			}
		}
		for _, k := range []string{"block", "rescue", "always"} {
			out = append(out, taskRoles(list(m[k]))...)
		}
	}
	return out
}

// roleDir finds a role in the playbook's roles/, then the project's.
func roleDir(projectDir, playbook, role string) string {
	if strings.Contains(role, "/") || strings.HasPrefix(role, ".") {
		return "" // a path or something odd: not resolved
	}
	for _, d := range []string{filepath.Join(projectDir, filepath.FromSlash(path.Dir(playbook)), "roles", role), filepath.Join(projectDir, "roles", role)} {
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d
		}
	}
	return ""
}

// RoleClosure adds the roles each role depends on (meta/main.yml), in order:
// dependencies before the role that needs them.
func RoleClosure(projectDir, playbook string, roles []string) []string {
	var out []string
	seen := map[string]bool{}
	var visit func(r string, depth int)
	visit = func(r string, depth int) {
		if seen[r] || depth > 10 {
			return
		}
		seen[r] = true
		if dir := roleDir(projectDir, playbook, r); dir != "" {
			if m := readYAMLMap(filepath.Join(dir, "meta"), "main"); m != nil {
				for _, d := range list(m["dependencies"]) {
					switch x := d.(type) {
					case string:
						visit(x, depth+1)
					case map[string]any:
						if n := firstString(x, "role", "name"); n != "" {
							visit(n, depth+1)
						}
					}
				}
			}
		}
		out = append(out, r)
	}
	for _, r := range roles {
		visit(r, 0)
	}
	return out
}

// ---- host patterns ----

// Match resolves a play's host pattern against the inventory, the way
// Ansible does: plain terms are unioned, then &terms intersect, then !terms
// exclude. ok is false for patterns that can't be resolved statically
// (templated ones).
func (inv *Inventory) Match(pattern string) (hosts []string, ok bool) {
	if strings.Contains(pattern, "{{") {
		return nil, false
	}
	var plain, and, not []string
	for _, t := range splitPattern(pattern) {
		switch {
		case strings.HasPrefix(t, "&"):
			and = append(and, t[1:])
		case strings.HasPrefix(t, "!"):
			not = append(not, t[1:])
		default:
			plain = append(plain, t)
		}
	}
	if len(plain) == 0 {
		plain = []string{"all"}
	}
	set := map[string]bool{}
	for _, t := range plain {
		for _, h := range inv.term(t) {
			set[h] = true
		}
	}
	for _, t := range and {
		keep := map[string]bool{}
		for _, h := range inv.term(t) {
			keep[h] = set[h]
		}
		set = keep
	}
	for _, t := range not {
		for _, h := range inv.term(t) {
			delete(set, h)
		}
	}
	for h, in := range set {
		if in {
			hosts = append(hosts, h)
		}
	}
	sort.Strings(hosts)
	return hosts, true
}

// splitPattern splits on ',' and on ':' (the older separator). IPv6
// addresses and ranges are rare in plays; they're not special-cased.
func splitPattern(p string) []string {
	var out []string
	for _, a := range strings.Split(p, ",") {
		for _, b := range strings.Split(a, ":") {
			if b = strings.TrimSpace(b); b != "" {
				out = append(out, b)
			}
		}
	}
	return out
}

func (inv *Inventory) term(t string) []string {
	if t == "all" || t == "*" {
		return inv.MembersOf("all")
	}
	if _, ok := inv.Groups[t]; ok {
		return inv.MembersOf(t)
	}
	var match func(string) bool
	switch {
	case strings.HasPrefix(t, "~"):
		re, err := regexp.Compile(t[1:])
		if err != nil {
			return nil
		}
		match = re.MatchString
	case strings.ContainsAny(t, "*?["):
		match = func(s string) bool { ok, _ := path.Match(t, s); return ok }
	default:
		match = func(s string) bool { return s == t }
	}
	set := map[string]bool{}
	for g := range inv.Groups {
		if match(g) {
			for _, h := range inv.MembersOf(g) {
				set[h] = true
			}
		}
	}
	for _, h := range inv.Hosts {
		if match(h) {
			set[h] = true
		}
	}
	out := make([]string, 0, len(set))
	for h := range set {
		out = append(out, h)
	}
	return out
}

// ---- ports ----

// HostPort is a port a host's variables configure.
type HostPort struct {
	Port  int
	Proto string
	Var   string
	Role  string // set when the value comes from the role's defaults or vars
}

var portVar = regexp.MustCompile(`(^|_)ports?$`)

// noPortConn: connections that don't go over the network to the host.
var noPortConn = map[string]bool{"local": true, "docker": true, "community.docker.docker": true, "podman": true, "containers.podman.podman": true, "chroot": true, "lxd": true, "community.general.lxd": true}

// DeclaredPorts reads *_port and *_ports variables a host ends up with:
// role defaults, then the inventory's values (ansible-inventory's hostvars),
// then role vars, each overriding the one before. ansible_port (default 22)
// is reported as ssh unless the host is reached without the network.
func DeclaredPorts(projectDir string, inv *Inventory, host string, roles []RoleRef) []HostPort {
	type val struct {
		v    any
		role string
	}
	vars := map[string]val{}
	setAll := func(m map[string]any, role string) {
		for k, v := range m {
			vars[k] = val{v, role}
		}
	}
	for _, r := range roles {
		if dir := roleDir(projectDir, r.Playbook, r.Name); dir != "" {
			setAll(readYAMLMap(filepath.Join(dir, "defaults"), "main"), r.Name)
		}
	}
	setAll(inv.HostVars[host], "")
	for _, r := range roles {
		if dir := roleDir(projectDir, r.Playbook, r.Name); dir != "" {
			setAll(readYAMLMap(filepath.Join(dir, "vars"), "main"), r.Name)
		}
	}
	lookup := func(name string) any { return vars[name].v }

	var out []HostPort
	if conn, _ := vars["ansible_connection"].v.(string); !noPortConn[conn] {
		p := 22
		if n, ok := portNumber(resolveRef(vars["ansible_port"].v, lookup)); ok {
			p = n
		}
		out = append(out, HostPort{Port: p, Proto: "tcp", Var: "ssh (ansible_port)"})
	}
	names := make([]string, 0, len(vars))
	for k := range vars {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if strings.HasPrefix(k, "ansible_") || !portVar.MatchString(k) {
			continue
		}
		proto := "tcp"
		if strings.Contains(k, "udp") {
			proto = "udp"
		}
		v := resolveRef(vars[k].v, lookup)
		items := list(v)
		if items == nil {
			items = []any{v}
		}
		for _, it := range items {
			p, pr := it, proto
			if s, ok := it.(string); ok { // "8080/udp"
				if a, b, cut := strings.Cut(s, "/"); cut {
					p, pr = a, strings.ToLower(b)
				}
			}
			if n, ok := portNumber(resolveRef(p, lookup)); ok {
				out = append(out, HostPort{Port: n, Proto: pr, Var: k, Role: vars[k].role})
			}
		}
	}
	return out
}

// RoleRef names a role and the playbook it was found through (for role paths).
type RoleRef struct{ Name, Playbook string }

var simpleRef = regexp.MustCompile(`^\{\{\s*([A-Za-z_][A-Za-z0-9_]*)\s*\}\}$`)

// resolveRef follows "{{ other_var }}" a few times; anything more templated stays unresolved.
func resolveRef(v any, lookup func(string) any) any {
	for i := 0; i < 5; i++ {
		s, ok := v.(string)
		if !ok {
			return v
		}
		m := simpleRef.FindStringSubmatch(strings.TrimSpace(s))
		if m == nil {
			return v
		}
		v = lookup(m[1])
	}
	return v
}

func portNumber(v any) (int, bool) {
	var n int
	switch x := v.(type) {
	case int:
		n = x
	case float64:
		n = int(x)
	case string:
		var err error
		if n, err = strconv.Atoi(strings.TrimSpace(x)); err != nil {
			return 0, false
		}
	default:
		return 0, false
	}
	return n, n > 0 && n <= 65535
}

// readYAMLMap reads dir/<base>.yml, dir/<base>.yaml or dir/<base>/*.yml as one map.
func readYAMLMap(dir, base string) map[string]any {
	files := []string{filepath.Join(dir, base+".yml"), filepath.Join(dir, base+".yaml")}
	more, _ := filepath.Glob(filepath.Join(dir, base, "*.yml"))
	files = append(files, more...)
	var out map[string]any
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil || strings.HasPrefix(string(b), vaultHeader) {
			continue
		}
		var m map[string]any
		if yaml.Unmarshal(b, &m) != nil {
			continue
		}
		if out == nil {
			out = map[string]any{}
		}
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

func list(v any) []any { l, _ := v.([]any); return l }

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}
