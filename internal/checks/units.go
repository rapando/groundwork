package checks

import (
	"path"
	"sort"
	"strings"

	"github.com/rapando/groundwork/internal/config"
	"github.com/rapando/groundwork/internal/workspace"
)

const (
	KindRoot    = "terraform-root"
	KindModule  = "terraform-module"
	KindAnsible = "ansible"
)

// Unit is the smallest thing checks run against: a Terraform root, a module
// directory, or an Ansible project.
type Unit struct {
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Path      string   `json:"path"`
	Tools     []string `json:"tools"`
	Deps      []string `json:"-"` // module dirs a root's validate depends on (transitive)
	Playbooks []string `json:"-"` // repo-relative
}

var toolsByKind = map[string][]string{
	KindRoot:    {"fmt", "validate", "tflint", "checkov", "secrets"},
	KindModule:  {"fmt", "tflint", "checkov", "secrets"},
	KindAnsible: {"yamllint", "ansible-lint", "syntax-check", "secrets"},
}

func unitID(kind, p string) string {
	if kind == KindAnsible {
		return "ans:" + p
	}
	return "tf:" + p
}

// BuildUnits derives units from the config and a fresh detection report.
func BuildUnits(cfg *config.Config, rep *workspace.Report) []Unit {
	if cfg == nil {
		return nil
	}
	enabled := map[string]bool{}
	for _, c := range cfg.Checks.Enabled {
		enabled[c] = true
	}
	pick := func(kind string) []string {
		var out []string
		for _, t := range toolsByKind[kind] {
			if enabled[t] {
				out = append(out, t)
			}
		}
		return out
	}

	// module → modules/roots that use it, inverted to dir → modules it uses.
	uses := map[string][]string{}
	moduleDirs := map[string]bool{}
	for _, m := range rep.TerraformModules {
		moduleDirs[m.Path] = true
		for _, by := range m.ReferencedBy {
			uses[by] = append(uses[by], m.Path)
		}
	}
	closure := func(start string) []string {
		seen := map[string]bool{}
		queue := append([]string(nil), uses[start]...)
		var out []string
		for len(queue) > 0 {
			d := queue[0]
			queue = queue[1:]
			if seen[d] {
				continue
			}
			seen[d] = true
			out = append(out, d)
			queue = append(queue, uses[d]...)
		}
		sort.Strings(out)
		return out
	}

	var units []Unit
	for _, r := range cfg.Terraform.Roots {
		p := path.Clean(r.Path)
		units = append(units, Unit{ID: unitID(KindRoot, p), Kind: KindRoot, Path: p, Tools: pick(KindRoot), Deps: closure(p)})
	}
	seen := map[string]bool{}
	for _, pat := range cfg.Terraform.Modules {
		for _, m := range expandModules(pat, moduleDirs) {
			if seen[m] {
				continue
			}
			seen[m] = true
			units = append(units, Unit{ID: unitID(KindModule, m), Kind: KindModule, Path: m, Tools: pick(KindModule), Deps: closure(m)})
		}
	}
	for _, p := range cfg.Ansible.Projects {
		pp := path.Clean(p.Path)
		u := Unit{ID: unitID(KindAnsible, pp), Kind: KindAnsible, Path: pp, Tools: pick(KindAnsible)}
		for _, ap := range rep.Ansible {
			if ap.Path == pp {
				u.Playbooks = ap.Playbooks
			}
		}
		units = append(units, u)
	}
	// units whose tools are all disabled are dropped
	out := units[:0]
	for _, u := range units {
		if len(u.Tools) > 0 {
			out = append(out, u)
		}
	}
	return out
}

func expandModules(pat string, known map[string]bool) []string {
	var out []string
	if parent, ok := strings.CutSuffix(pat, "/*"); ok {
		for m := range known {
			if path.Dir(m) == path.Clean(parent) {
				out = append(out, m)
			}
		}
	} else if known[path.Clean(pat)] {
		out = append(out, path.Clean(pat))
	}
	sort.Strings(out)
	return out
}

// Owner returns the deepest unit containing file, or nil.
func Owner(units []Unit, file string) *Unit {
	var best *Unit
	for i := range units {
		u := &units[i]
		if file == u.Path || strings.HasPrefix(file, u.Path+"/") || u.Path == "." {
			if best == nil || len(u.Path) > len(best.Path) {
				best = u
			}
		}
	}
	return best
}

// Affected returns every unit that must re-run when file changes: its owner,
// plus roots whose validation includes the module the file belongs to.
func Affected(units []Unit, file string) []Unit {
	var out []Unit
	seen := map[string]bool{}
	add := func(u Unit) {
		if !seen[u.ID] {
			seen[u.ID] = true
			out = append(out, u)
		}
	}
	if o := Owner(units, file); o != nil {
		add(*o)
	}
	for _, u := range units {
		for _, d := range u.Deps {
			if file == d || strings.HasPrefix(file, d+"/") {
				add(u)
			}
		}
	}
	return out
}
