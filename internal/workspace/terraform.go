package workspace

import (
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
)

type tfScan struct {
	backend       string
	backendEv     *Evidence
	providers     []string
	providerEv    *Evidence
	reqProviders  *Evidence
	hasModuleCall bool
	counts        map[string]int // block type → count
	moduleSources []string       // resolved repo-relative dirs
}

func scanTerraformDir(root, dir string, names []string) *tfScan {
	s := &tfScan{counts: map[string]int{}}
	for _, n := range names {
		if !strings.HasSuffix(n, ".tf") {
			continue
		}
		rel := path.Join(dir, n)
		src, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			continue
		}
		f, _ := hclsyntax.ParseConfig(src, rel, hcl.Pos{Line: 1, Column: 1})
		if f == nil {
			continue
		}
		body, ok := f.Body.(*hclsyntax.Body)
		if !ok {
			continue
		}
		for _, b := range body.Blocks {
			s.counts[b.Type]++
			line := b.TypeRange.Start.Line
			switch b.Type {
			case "terraform":
				for _, ib := range b.Body.Blocks {
					il := ib.TypeRange.Start.Line
					switch ib.Type {
					case "backend":
						if len(ib.Labels) > 0 && s.backendEv == nil {
							s.backend = ib.Labels[0]
							s.backendEv = &Evidence{rel, il, `backend "` + ib.Labels[0] + `" block`}
						}
					case "cloud":
						if s.backendEv == nil {
							s.backend = "cloud"
							s.backendEv = &Evidence{rel, il, "cloud block"}
						}
					case "required_providers":
						if s.reqProviders == nil {
							s.reqProviders = &Evidence{rel, il, "required_providers block"}
						}
					}
				}
			case "provider":
				if len(b.Labels) > 0 {
					s.providers = appendUnique(s.providers, b.Labels[0])
					if s.providerEv == nil {
						s.providerEv = &Evidence{rel, line, `provider "` + b.Labels[0] + `" block`}
					}
				}
			case "module":
				s.hasModuleCall = true
				if attr, ok := b.Body.Attributes["source"]; ok {
					v, diags := attr.Expr.Value(nil)
					if !diags.HasErrors() && v.Type().FriendlyName() == "string" {
						src := v.AsString()
						if strings.HasPrefix(src, "./") || strings.HasPrefix(src, "../") {
							s.moduleSources = append(s.moduleSources, path.Join(dir, src))
						}
					}
				}
			}
		}
	}
	return s
}

func appendUnique(l []string, v string) []string {
	for _, x := range l {
		if x == v {
			return l
		}
	}
	return append(l, v)
}

// classifyTerraform returns roots and modules for all dirs with .tf files.
func classifyTerraform(root string, w *walkResult) (roots, modules []TFDir) {
	scans := map[string]*tfScan{}
	for dir, names := range w.byDir {
		for _, n := range names {
			if strings.HasSuffix(n, ".tf") {
				scans[dir] = scanTerraformDir(root, dir, names)
				break
			}
		}
	}
	referencedBy := map[string][]string{}
	for dir, s := range scans {
		for _, m := range s.moduleSources {
			referencedBy[m] = append(referencedBy[m], dir)
		}
	}
	dirs := make([]string, 0, len(scans))
	for d := range scans {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	for _, dir := range dirs {
		s := scans[dir]
		d := TFDir{Path: dir, Providers: s.providers, Backend: s.backend, Evidence: []Evidence{}}
		for _, n := range w.byDir[dir] {
			if strings.HasSuffix(n, ".tfvars") {
				d.Tfvars = append(d.Tfvars, path.Join(dir, n))
			}
		}
		// tfvars kept in conventional sibling dirs (env/, envs/, vars/) also count.
		for _, sub := range []string{"env", "envs", "environments", "vars"} {
			for _, n := range w.byDir[path.Join(dir, sub)] {
				if strings.HasSuffix(n, ".tfvars") {
					d.Tfvars = append(d.Tfvars, path.Join(dir, sub, n))
				}
			}
		}
		sort.Strings(d.Tfvars)

		if refs := referencedBy[dir]; len(refs) > 0 {
			sort.Strings(refs)
			d.Kind, d.ReferencedBy = "module", refs
			d.Evidence = append(d.Evidence, Evidence{refs[0], 0, `referenced as a module source by "` + refs[0] + `"`})
			modules = append(modules, d)
			continue
		}
		if s.backendEv != nil {
			d.Score += 5
			d.Evidence = append(d.Evidence, *s.backendEv)
		}
		if s.providerEv != nil {
			d.Score += 3
			d.Evidence = append(d.Evidence, *s.providerEv)
		}
		if s.reqProviders != nil {
			d.Score += 2
			d.Evidence = append(d.Evidence, *s.reqProviders)
		}
		if len(d.Tfvars) > 0 {
			d.Score += 2
			d.Evidence = append(d.Evidence, Evidence{d.Tfvars[0], 0, "tfvars file present"})
		}
		switch {
		case d.Score > 0:
			d.Kind = "root"
		case s.hasModuleCall:
			d.Kind, d.Score = "root", 1
			d.Evidence = append(d.Evidence, Evidence{dir, 0, "calls modules but declares no provider or backend"})
		default:
			d.Kind, d.Score = "module", 3
			d.Evidence = append(d.Evidence, Evidence{dir, 0, "only variable/output/resource blocks, no backend or provider"})
		}
		if d.Kind == "root" {
			roots = append(roots, d)
		} else {
			modules = append(modules, d)
		}
	}
	return roots, modules
}
