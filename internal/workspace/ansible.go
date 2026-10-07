package workspace

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

const maxYAMLBytes = 1 << 20

func isYAML(n string) bool { return strings.HasSuffix(n, ".yml") || strings.HasSuffix(n, ".yaml") }

func hasSeg(p string, segs ...string) bool {
	for _, s := range strings.Split(p, "/") {
		for _, t := range segs {
			if s == t {
				return true
			}
		}
	}
	return false
}

func readYAMLNode(root, rel string) *yaml.Node {
	full := filepath.Join(root, filepath.FromSlash(rel))
	if st, err := os.Stat(full); err != nil || st.Size() > maxYAMLBytes {
		return nil
	}
	b, err := os.ReadFile(full)
	if err != nil {
		return nil
	}
	var doc yaml.Node
	if yaml.Unmarshal(b, &doc) != nil || len(doc.Content) == 0 {
		return nil
	}
	return doc.Content[0]
}

func mapKeys(n *yaml.Node) map[string]*yaml.Node {
	out := map[string]*yaml.Node{}
	if n == nil || n.Kind != yaml.MappingNode {
		return out
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		out[n.Content[i].Value] = n.Content[i+1]
	}
	return out
}

// playbookLine returns the line of the first play when n looks like a playbook.
func playbookLine(n *yaml.Node) int {
	if n == nil || n.Kind != yaml.SequenceNode {
		return 0
	}
	for _, item := range n.Content {
		k := mapKeys(item)
		if _, ok := k["import_playbook"]; ok {
			return item.Line
		}
		if _, ok := k["hosts"]; !ok {
			continue
		}
		for _, sig := range []string{"tasks", "roles", "pre_tasks", "post_tasks", "import_playbook"} {
			if _, ok := k[sig]; ok {
				return item.Line
			}
		}
	}
	return 0
}

func looksLikeInventoryYAML(n *yaml.Node) bool {
	k := mapKeys(n)
	if all, ok := k["all"]; ok {
		ak := mapKeys(all)
		_, c := ak["children"]
		_, h := ak["hosts"]
		_, v := ak["vars"]
		return c || h || v
	}
	_, c := k["children"]
	return c
}

type ansArtefacts struct {
	cfgs      []string
	playbooks map[string]int // path → line
	roles     []string       // role dirs (…/roles/<name>)
	invs      []string
	varsDirs  []string
}

func scanAnsible(root string, w *walkResult) *ansArtefacts {
	a := &ansArtefacts{playbooks: map[string]int{}}
	for _, rel := range w.files {
		base := path.Base(rel)
		dir := path.Dir(rel)
		switch {
		case base == "ansible.cfg":
			a.cfgs = append(a.cfgs, rel)
		case strings.HasSuffix(rel, "/tasks/main.yml") || strings.HasSuffix(rel, "/tasks/main.yaml"):
			parent := path.Dir(dir) // roles/<name>
			if path.Base(path.Dir(parent)) == "roles" {
				a.roles = append(a.roles, parent)
			}
		}
		if (path.Base(dir) == "group_vars" || path.Base(dir) == "host_vars") && !contains(a.varsDirs, dir) {
			a.varsDirs = append(a.varsDirs, dir)
		}
		// inventories
		inInvDir := hasSeg(dir, "inventory", "inventories")
		inVars := hasSeg(rel, "group_vars", "host_vars")
		if !inVars {
			ext := path.Ext(base)
			switch {
			case base == "hosts" || base == "hosts.ini" || base == "hosts.yml" || base == "hosts.yaml":
				if ext == ".yml" || ext == ".yaml" {
					if looksLikeInventoryYAML(readYAMLNode(root, rel)) {
						a.invs = append(a.invs, rel)
					}
				} else {
					a.invs = append(a.invs, rel)
				}
			case inInvDir && (ext == ".ini" || ext == ".py" || ext == ""):
				a.invs = append(a.invs, rel)
			case inInvDir && isYAML(base):
				if looksLikeInventoryYAML(readYAMLNode(root, rel)) {
					a.invs = append(a.invs, rel)
				}
			}
		}
		// playbooks
		if isYAML(base) && !inVars && !hasSeg(rel, "roles", ".github") && !contains(a.invs, rel) &&
			base != ".gitlab-ci.yml" && !inInvDir {
			if l := playbookLine(readYAMLNode(root, rel)); l > 0 {
				a.playbooks[rel] = l
			}
		}
	}
	return a
}

func contains(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

func parentOrDot(p string) string {
	d := path.Dir(p)
	if d == "" {
		return "."
	}
	return d
}

func under(dir, p string) bool {
	return dir == "." || p == dir || strings.HasPrefix(p, dir+"/")
}

func buildAnsibleProjects(a *ansArtefacts) []AnsibleProject {
	cand := map[string]bool{}
	for _, c := range a.cfgs {
		cand[parentOrDot(c)] = true
	}
	for _, r := range a.roles {
		cand[parentOrDot(path.Dir(r))] = true // roles/ → its parent
	}
	for _, i := range a.invs {
		cand[inventoryRoot(i)] = true
	}
	for _, v := range a.varsDirs {
		cand[inventoryRoot(v)] = true
	}
	for p := range a.playbooks {
		d := parentOrDot(p)
		if path.Base(d) == "playbooks" {
			d = parentOrDot(d)
		}
		cand[d] = true
	}
	// inventory-owned vars live under the inventory dir; map to its project.
	dirs := make([]string, 0, len(cand))
	for d := range cand {
		dirs = append(dirs, d)
	}
	sort.Slice(dirs, func(i, j int) bool {
		return len(dirs[i]) > len(dirs[j]) || (len(dirs[i]) == len(dirs[j]) && dirs[i] < dirs[j])
	})
	owner := func(p string) string { // deepest candidate containing p
		for _, d := range dirs {
			if under(d, p) {
				return d
			}
		}
		return ""
	}
	projects := map[string]*AnsibleProject{}
	get := func(d string) *AnsibleProject {
		if projects[d] == nil {
			projects[d] = &AnsibleProject{Path: d, Evidence: []Evidence{}}
		}
		return projects[d]
	}
	for _, c := range a.cfgs {
		p := get(owner(c))
		p.Config = c
		p.Evidence = append(p.Evidence, Evidence{c, 0, "ansible.cfg"})
	}
	for pb, line := range a.playbooks {
		o := owner(pb)
		if o == "" {
			continue
		}
		p := get(o)
		p.Playbooks = append(p.Playbooks, pb)
		p.Evidence = append(p.Evidence, Evidence{pb, line, "playbook (hosts + tasks/roles)"})
	}
	for _, r := range a.roles {
		if o := owner(r); o != "" {
			p := get(o)
			p.Roles = append(p.Roles, r)
			p.Evidence = append(p.Evidence, Evidence{r + "/tasks/main.yml", 0, "role " + path.Base(r)})
		}
	}
	for _, i := range a.invs {
		if o := owner(i); o != "" {
			p := get(o)
			p.Inventories = append(p.Inventories, i)
			p.Evidence = append(p.Evidence, Evidence{i, 0, "inventory"})
		}
	}
	for _, v := range a.varsDirs {
		if o := owner(v); o != "" {
			p := get(o)
			p.VarsDirs = append(p.VarsDirs, v)
		}
	}
	out := make([]AnsibleProject, 0, len(projects))
	for _, p := range projects {
		sort.Strings(p.Playbooks)
		sort.Strings(p.Roles)
		sort.Strings(p.Inventories)
		sort.Strings(p.VarsDirs)
		sort.SliceStable(p.Evidence, func(i, j int) bool { return p.Evidence[i].File < p.Evidence[j].File })
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// inventoryRoot returns the project dir an inventory file belongs to: the
// parent of the nearest inventory/ or inventories/ ancestor, else its own dir.
func inventoryRoot(file string) string {
	for d := parentOrDot(file); d != "." && d != "/"; d = parentOrDot(d) {
		if b := path.Base(d); b == "inventory" || b == "inventories" {
			return parentOrDot(d)
		}
	}
	return parentOrDot(file)
}
