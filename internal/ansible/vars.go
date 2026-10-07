package ansible

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Source is one place a variable is set.
type Source struct {
	File  string `json:"file"`  // repo-relative
	Level string `json:"level"` // e.g. "inventory group_vars/web"
	Value string `json:"value"` // rendered, masked if secret
}

// VarRow is one variable on one host: the winning source first, then the
// sources it overrides (next-highest first).
type VarRow struct {
	Name       string   `json:"name"`
	Value      string   `json:"value"`
	Winner     *Source  `json:"winner,omitempty"`
	Overridden []Source `json:"overridden"`
	// ok: groundwork's resolution agrees with ansible-inventory.
	// unknown: they disagree, or ansible reports a value groundwork can't place.
	// role-default: only set in role defaults (applies when the role runs).
	Status string `json:"status"`
	Secret bool   `json:"secret,omitempty"`
}

type HostVars struct {
	Rows           []VarRow `json:"rows"`
	EncryptedFiles []string `json:"encrypted_files,omitempty"` // vault files whose keys can't be listed
}

const vaultHeader = "$ANSIBLE_VAULT;"

var secretKey = regexp.MustCompile(`(?i)(pass(word|wd)?|secret|token|api[_-]?key|private[_-]?key|credential)`)

type vaultValue struct{}

func render(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case string:
		return x
	case vaultValue:
		return "(vault encrypted)"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "?"
	}
	s := string(b)
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

// norm makes YAML-decoded and JSON-decoded values comparable.
func norm(v any) string {
	if _, ok := v.(vaultValue); ok {
		return "__vault__"
	}
	if m, ok := v.(map[string]any); ok {
		if _, isVault := m["__ansible_vault"]; isVault {
			return "__vault__"
		}
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// decodeYAML reads a vars file, turning `!vault` scalars into vaultValue.
func decodeYAML(b []byte) (map[string]any, bool) {
	if strings.HasPrefix(strings.TrimSpace(string(b)), vaultHeader) {
		return nil, true
	}
	var root yaml.Node
	if yaml.Unmarshal(b, &root) != nil || len(root.Content) == 0 {
		return map[string]any{}, false
	}
	v := nodeValue(root.Content[0])
	m, _ := v.(map[string]any)
	if m == nil {
		m = map[string]any{}
	}
	return m, false
}

func nodeValue(n *yaml.Node) any {
	if n.Tag == "!vault" {
		return vaultValue{}
	}
	switch n.Kind {
	case yaml.MappingNode:
		m := map[string]any{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			m[n.Content[i].Value] = nodeValue(n.Content[i+1])
		}
		return m
	case yaml.SequenceNode:
		l := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			l = append(l, nodeValue(c))
		}
		return l
	case yaml.AliasNode:
		return nodeValue(n.Alias)
	}
	var v any
	_ = n.Decode(&v)
	return v
}

// varsAt loads <dir>/<kind>/<name>{,.yml,.yaml,.json} or the directory form
// <dir>/<kind>/<name>/*.
func varsAt(dir, kind, name string) (files []string, vars []map[string]any, encrypted []string) {
	base := filepath.Join(dir, kind, name)
	var candidates []string
	if st, err := os.Stat(base); err == nil && st.IsDir() {
		entries, _ := os.ReadDir(base)
		for _, e := range entries {
			if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				candidates = append(candidates, filepath.Join(base, e.Name()))
			}
		}
		sort.Strings(candidates)
	} else {
		for _, ext := range []string{"", ".yml", ".yaml", ".json"} {
			if st, err := os.Stat(base + ext); err == nil && !st.IsDir() {
				candidates = append(candidates, base+ext)
			}
		}
	}
	for _, f := range candidates {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		m, enc := decodeYAML(b)
		if enc {
			encrypted = append(encrypted, f)
			continue
		}
		files = append(files, f)
		vars = append(vars, m)
	}
	return files, vars, encrypted
}

// inlineVars are vars set inside the inventory file itself.
type inlineVars struct {
	group map[string]map[string]any
	host  map[string]map[string]any
}

func parseInventoryFile(path string) inlineVars {
	iv := inlineVars{group: map[string]map[string]any{}, host: map[string]map[string]any{}}
	b, err := os.ReadFile(path)
	if err != nil {
		return iv
	}
	ext := strings.ToLower(filepath.Ext(path))
	if ext == ".yml" || ext == ".yaml" {
		m, _ := decodeYAML(b)
		var walk func(name string, def any)
		walk = func(name string, def any) {
			g, _ := def.(map[string]any)
			if g == nil {
				return
			}
			if vars, ok := g["vars"].(map[string]any); ok {
				if iv.group[name] == nil {
					iv.group[name] = map[string]any{}
				}
				for k, v := range vars {
					iv.group[name][k] = v
				}
			}
			if hosts, ok := g["hosts"].(map[string]any); ok {
				for h, hv := range hosts {
					if vars, ok := hv.(map[string]any); ok {
						if iv.host[h] == nil {
							iv.host[h] = map[string]any{}
						}
						for k, v := range vars {
							iv.host[h][k] = v
						}
					}
				}
			}
			if children, ok := g["children"].(map[string]any); ok {
				for c, cd := range children {
					walk(c, cd)
				}
			}
		}
		for name, def := range m {
			walk(name, def)
		}
		return iv
	}
	// INI: [group], [group:vars], [group:children]; host lines "name k=v ..."
	section, kind := "ungrouped", "hosts"
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section, kind = strings.Trim(line, "[]"), "hosts"
			if i := strings.Index(section, ":"); i >= 0 {
				section, kind = section[:i], section[i+1:]
			}
			continue
		}
		switch kind {
		case "vars":
			if k, v, ok := strings.Cut(line, "="); ok {
				if iv.group[section] == nil {
					iv.group[section] = map[string]any{}
				}
				iv.group[section][strings.TrimSpace(k)] = iniValue(strings.TrimSpace(v))
			}
		case "hosts":
			fields := strings.Fields(line)
			if len(fields) == 0 {
				continue
			}
			for _, f := range fields[1:] {
				if k, v, ok := strings.Cut(f, "="); ok {
					if iv.host[fields[0]] == nil {
						iv.host[fields[0]] = map[string]any{}
					}
					iv.host[fields[0]][k] = iniValue(v)
				}
			}
		}
	}
	return iv
}

func iniValue(s string) any {
	s = strings.Trim(s, `"'`)
	var v any
	if json.Unmarshal([]byte(s), &v) == nil {
		return v
	}
	return s
}

type layer struct {
	level string
	file  string
	vars  map[string]any
}

// Provenance explains where each variable on host comes from.
//
//	repoRoot/projectDir: absolute paths; inventory: absolute path to the
//	inventory file or directory; actual: `ansible-inventory --host` output.
//
// Ladder (low → high), as documented by Ansible: role defaults, inventory-file
// group vars, inventory group_vars/all, playbook group_vars/all, inventory
// group_vars/*, playbook group_vars/*, inventory-file host vars, inventory
// host_vars/*, playbook host_vars/*.
func Provenance(repoRoot, projectDir, inventory string, inv *Inventory, host string, actual map[string]any) HostVars {
	rel := func(p string) string {
		r, err := filepath.Rel(repoRoot, p)
		if err != nil {
			return p
		}
		return filepath.ToSlash(r)
	}
	invDir := inventory
	var invFiles []string
	if st, err := os.Stat(inventory); err == nil && !st.IsDir() {
		invDir = filepath.Dir(inventory)
		invFiles = []string{inventory}
	} else if err == nil {
		entries, _ := os.ReadDir(inventory)
		for _, e := range entries {
			if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				invFiles = append(invFiles, filepath.Join(inventory, e.Name()))
			}
		}
	}
	groups := inv.GroupsOf(host)

	var layers []layer
	var encrypted []string
	seenFile := map[string]bool{}
	addFiles := func(level string, files []string, vars []map[string]any, enc []string) {
		for i, f := range files {
			if seenFile[f] { // inventory dir == playbook dir: the same file is one layer, at its highest level
				for j := range layers {
					if layers[j].file == rel(f) {
						layers[j].vars = nil
					}
				}
			}
			seenFile[f] = true
			layers = append(layers, layer{level, rel(f), vars[i]})
		}
		for _, e := range enc {
			if !seenFile[e] {
				seenFile[e] = true
				encrypted = append(encrypted, rel(e))
			}
		}
	}

	// 1. role defaults
	roleDefaults, _ := filepath.Glob(filepath.Join(projectDir, "roles", "*", "defaults", "main.y*ml"))
	sort.Strings(roleDefaults)
	for _, f := range roleDefaults {
		if b, err := os.ReadFile(f); err == nil {
			m, enc := decodeYAML(b)
			if !enc {
				role := filepath.Base(filepath.Dir(filepath.Dir(f)))
				layers = append(layers, layer{"role default (" + role + ")", rel(f), m})
			}
		}
	}
	// 2. inventory-file group vars
	inline := map[string]inlineVars{}
	for _, f := range invFiles {
		inline[f] = parseInventoryFile(f)
	}
	for _, g := range groups {
		for _, f := range invFiles {
			if v := inline[f].group[g.Name]; len(v) > 0 {
				layers = append(layers, layer{"inventory file, group " + g.Name, rel(f), v})
			}
		}
	}
	// 3.-6. group_vars: all first (inventory, playbook), then the rest by depth
	files, vars, enc := varsAt(invDir, "group_vars", "all")
	addFiles("inventory group_vars/all", files, vars, enc)
	files, vars, enc = varsAt(projectDir, "group_vars", "all")
	addFiles("playbook group_vars/all", files, vars, enc)
	for _, g := range groups {
		if g.Name == "all" {
			continue
		}
		files, vars, enc = varsAt(invDir, "group_vars", g.Name)
		addFiles("inventory group_vars/"+g.Name, files, vars, enc)
	}
	for _, g := range groups {
		if g.Name == "all" {
			continue
		}
		files, vars, enc = varsAt(projectDir, "group_vars", g.Name)
		addFiles("playbook group_vars/"+g.Name, files, vars, enc)
	}
	// 7.-9. host vars
	for _, f := range invFiles {
		if v := inline[f].host[host]; len(v) > 0 {
			layers = append(layers, layer{"inventory file, host", rel(f), v})
		}
	}
	files, vars, enc = varsAt(invDir, "host_vars", host)
	addFiles("inventory host_vars", files, vars, enc)
	files, vars, enc = varsAt(projectDir, "host_vars", host)
	addFiles("playbook host_vars", files, vars, enc)

	// fold: per variable, sources in ascending precedence
	type src struct {
		layer
		value any
	}
	byName := map[string][]src{}
	for _, l := range layers {
		for k, v := range l.vars {
			byName[k] = append(byName[k], src{l, v})
		}
	}
	for k := range actual {
		if _, ok := byName[k]; !ok {
			byName[k] = nil
		}
	}
	names := make([]string, 0, len(byName))
	for k := range byName {
		names = append(names, k)
	}
	sort.Slice(names, func(i, j int) bool {
		ai, aj := strings.HasPrefix(names[i], "ansible_"), strings.HasPrefix(names[j], "ansible_")
		if ai != aj {
			return !ai // connection settings last
		}
		return names[i] < names[j]
	})

	out := HostVars{Rows: []VarRow{}, EncryptedFiles: encrypted}
	for _, name := range names {
		srcs := byName[name]
		secret := secretKey.MatchString(name)
		show := func(v any) string {
			if _, isVault := v.(vaultValue); isVault {
				return "(vault encrypted)"
			}
			if secret {
				return "••••"
			}
			return render(v)
		}
		row := VarRow{Name: name, Secret: secret, Overridden: []Source{}}
		av, inActual := actual[name]
		if len(srcs) == 0 {
			row.Status, row.Value = "unknown", show(av)
			out.Rows = append(out.Rows, row)
			continue
		}
		top := srcs[len(srcs)-1]
		row.Winner = &Source{File: top.file, Level: top.level, Value: show(top.value)}
		row.Value = row.Winner.Value
		for i := len(srcs) - 2; i >= 0; i-- {
			row.Overridden = append(row.Overridden, Source{File: srcs[i].file, Level: srcs[i].level, Value: show(srcs[i].value)})
		}
		switch {
		case strings.HasPrefix(top.level, "role default"):
			row.Status = "role-default"
			if inActual { // also set somewhere we didn't see
				row.Status = "unknown"
			}
		case !inActual:
			row.Status = "unknown"
		case norm(av) != norm(top.value):
			row.Status, row.Value = "unknown", show(av) // trust ansible's value; don't guess its source
		default:
			row.Status = "ok"
		}
		out.Rows = append(out.Rows, row)
	}
	return out
}

// DefaultInventory reads `inventory = …` from an ansible.cfg (or "").
func DefaultInventory(cfgPath string) string {
	b, err := os.ReadFile(cfgPath)
	if err != nil {
		return ""
	}
	inDefaults := false
	for _, l := range strings.Split(string(b), "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "[") {
			inDefaults = l == "[defaults]"
			continue
		}
		if k, v, ok := strings.Cut(l, "="); ok && inDefaults && strings.TrimSpace(k) == "inventory" {
			return strings.TrimSpace(strings.SplitN(v, ",", 2)[0])
		}
	}
	return ""
}
