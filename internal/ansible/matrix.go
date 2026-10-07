package ansible

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// MatrixCell is one group variable in one environment.
type MatrixCell struct {
	State  string `json:"state"`            // set | vault | absent
	Value  string `json:"value,omitempty"`  // rendered; masked when Secret
	Source string `json:"source,omitempty"` // e.g. "inventory group_vars/web"
	File   string `json:"file,omitempty"`   // repo-relative
	Line   int    `json:"line,omitempty"`
}

// MatrixRow is one (group, variable) pair across environments.
type MatrixRow struct {
	Group   string                `json:"group"`
	Name    string                `json:"name"`
	Secret  bool                  `json:"secret,omitempty"`
	Differs bool                  `json:"differs,omitempty"`
	Missing bool                  `json:"missing,omitempty"` // set in some envs only
	Cells   map[string]MatrixCell `json:"cells"`
}

type GroupMatrix struct {
	Envs           []string            `json:"envs"`
	Rows           []MatrixRow         `json:"rows"`
	EncryptedFiles map[string][]string `json:"encrypted_files,omitempty"` // env → whole-file vault files
}

func inventoryLocation(inventory string) (dir string, files []string) {
	st, err := os.Stat(inventory)
	if err != nil {
		return filepath.Dir(inventory), nil
	}
	if !st.IsDir() {
		return filepath.Dir(inventory), []string{inventory}
	}
	entries, _ := os.ReadDir(inventory)
	for _, e := range entries {
		if !e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			files = append(files, filepath.Join(inventory, e.Name()))
		}
	}
	return inventory, files
}

// keyLines maps each top-level key of a YAML mapping to its line.
func keyLines(path string) map[string]int {
	out := map[string]int{}
	b, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var root yaml.Node
	if yaml.Unmarshal(b, &root) != nil || len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return out
	}
	m := root.Content[0]
	for i := 0; i+1 < len(m.Content); i += 2 {
		out[m.Content[i].Value] = m.Content[i].Line
	}
	return out
}

func groupDirNames(dir string) []string {
	entries, _ := os.ReadDir(filepath.Join(dir, "group_vars"))
	var out []string
	for _, e := range entries {
		n := e.Name()
		if strings.HasPrefix(n, ".") {
			continue
		}
		if !e.IsDir() {
			n = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(n, ".yml"), ".yaml"), ".json")
		}
		out = append(out, n)
	}
	return out
}

// GroupVarsMatrix lays out group variables per environment. inventories maps
// env → absolute inventory path. Within one env the winning source follows
// Ansible's order for group-level vars: inventory-file vars < inventory
// group_vars < playbook-dir group_vars. Host vars aren't in the matrix: the
// Inventory screen explains them per host.
func GroupVarsMatrix(repoRoot, projectDir string, inventories map[string]string) GroupMatrix {
	rel := func(p string) string {
		r, err := filepath.Rel(repoRoot, p)
		if err != nil {
			return p
		}
		return filepath.ToSlash(r)
	}
	gm := GroupMatrix{EncryptedFiles: map[string][]string{}}
	for env := range inventories {
		gm.Envs = append(gm.Envs, env)
	}
	sort.Strings(gm.Envs)
	rows := map[[2]string]*MatrixRow{}
	lines := map[string]map[string]int{}
	lineOf := func(file, key string) int {
		if lines[file] == nil {
			lines[file] = keyLines(file)
		}
		return lines[file][key]
	}

	for _, env := range gm.Envs {
		invDir, invFiles := inventoryLocation(inventories[env])
		groups := map[string]bool{}
		inline := map[string]map[string]any{}
		inlineFile := map[string]string{}
		for _, f := range invFiles {
			iv := parseInventoryFile(f)
			for g, vs := range iv.group {
				groups[g] = true
				if inline[g] == nil {
					inline[g] = map[string]any{}
				}
				for k, v := range vs {
					inline[g][k] = v
				}
				inlineFile[g] = f
			}
		}
		dirs := []struct{ dir, level string }{{invDir, "inventory group_vars"}}
		if filepath.Clean(projectDir) != filepath.Clean(invDir) {
			dirs = append(dirs, struct{ dir, level string }{projectDir, "playbook group_vars"})
		}
		for _, d := range dirs {
			for _, g := range groupDirNames(d.dir) {
				groups[g] = true
			}
		}
		for g := range groups {
			set := func(k string, v any, cell MatrixCell) {
				key := [2]string{g, k}
				r := rows[key]
				if r == nil {
					r = &MatrixRow{Group: g, Name: k, Cells: map[string]MatrixCell{}}
					rows[key] = r
				}
				if _, isVault := v.(vaultValue); isVault {
					cell.State, cell.Value = "vault", "(vault encrypted)"
					r.Secret = true
				} else {
					cell.State, cell.Value = "set", render(v)
					if secretKey.MatchString(k) {
						r.Secret = true
					}
				}
				r.Cells[env] = cell
			}
			for k, v := range inline[g] {
				set(k, v, MatrixCell{Source: "inventory file", File: rel(inlineFile[g])})
			}
			for _, d := range dirs {
				files, vs, enc := varsAt(d.dir, "group_vars", g)
				for _, e := range enc {
					gm.EncryptedFiles[env] = append(gm.EncryptedFiles[env], rel(e))
				}
				for i, m := range vs {
					for k, v := range m {
						set(k, v, MatrixCell{Source: d.level + "/" + g, File: rel(files[i]), Line: lineOf(files[i], k)})
					}
				}
			}
		}
	}

	for _, r := range rows {
		vals := map[string]bool{}
		for _, env := range gm.Envs {
			c, ok := r.Cells[env]
			if !ok {
				r.Cells[env] = MatrixCell{State: "absent"}
				r.Missing = true
				continue
			}
			vals[c.Value] = true
		}
		r.Differs = len(vals) > 1
		if r.Secret {
			for env, c := range r.Cells {
				if c.State == "set" {
					c.Value = "••••••"
					r.Cells[env] = c
				}
			}
		}
		gm.Rows = append(gm.Rows, *r)
	}
	sort.Slice(gm.Rows, func(i, j int) bool {
		a, b := gm.Rows[i], gm.Rows[j]
		if (a.Group == "all") != (b.Group == "all") {
			return a.Group == "all"
		}
		if a.Group != b.Group {
			return a.Group < b.Group
		}
		return a.Name < b.Name
	})
	return gm
}
