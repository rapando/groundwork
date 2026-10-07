package workspace

import (
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var toolRe = regexp.MustCompile(`(?m)(^|[\s;&|(])(terraform|tofu|ansible-playbook)(\s|$)`)

// scanCI looks for IaC tool invocations in GitHub Actions and GitLab CI files.
func scanCI(root string, w *walkResult) []CIRef {
	var refs []CIRef
	for _, rel := range w.files {
		gh := strings.HasPrefix(rel, ".github/workflows/") && isYAML(rel)
		gl := rel == ".gitlab-ci.yml"
		if !gh && !gl {
			continue
		}
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		var doc yaml.Node
		if yaml.Unmarshal(b, &doc) != nil || len(doc.Content) == 0 {
			continue
		}
		walkCI(doc.Content[0], rel, "", "", &refs)
	}
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].File != refs[j].File {
			return refs[i].File < refs[j].File
		}
		return refs[i].Line < refs[j].Line
	})
	return refs
}

// walkCI visits mappings; a mapping with `run`/`script` is a step. working-directory
// is inherited from the enclosing `defaults.run` or the step itself.
func walkCI(n *yaml.Node, file, wd, _ string, out *[]CIRef) {
	switch n.Kind {
	case yaml.SequenceNode:
		for _, c := range n.Content {
			walkCI(c, file, wd, "", out)
		}
	case yaml.MappingNode:
		k := mapKeys(n)
		if d, ok := k["defaults"]; ok {
			if r, ok := mapKeys(d)["run"]; ok {
				if v, ok := mapKeys(r)["working-directory"]; ok {
					wd = v.Value
				}
			}
		}
		if v, ok := k["working-directory"]; ok && v.Kind == yaml.ScalarNode {
			wd = v.Value
		}
		for _, key := range []string{"run", "script"} {
			v, ok := k[key]
			if !ok {
				continue
			}
			var text string
			switch v.Kind {
			case yaml.ScalarNode:
				text = v.Value
			case yaml.SequenceNode:
				var parts []string
				for _, c := range v.Content {
					parts = append(parts, c.Value)
				}
				text = strings.Join(parts, "\n")
			}
			seen := map[string]bool{}
			for _, m := range toolRe.FindAllStringSubmatch(text, -1) {
				if tool := m[2]; !seen[tool] {
					seen[tool] = true
					*out = append(*out, CIRef{File: file, Line: v.Line, Tool: tool, WorkingDir: path.Clean(wd)})
				}
			}
		}
		for i := 1; i < len(n.Content); i += 2 {
			walkCI(n.Content[i], file, wd, "", out)
		}
	}
}
