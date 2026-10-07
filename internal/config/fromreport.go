package config

import (
	"path"
	"sort"
	"strings"

	"github.com/rapando/groundwork/internal/workspace"
)

// FromReport derives a starting config from detection results.
func FromReport(r *workspace.Report) *Config {
	c := &Config{
		Version: 1,
		Mode:    r.Mode,
		Checks:  Checks{OnSave: true},
		Ignore:  []string{"vendor/", "node_modules/", "**/.terraform/"},
	}
	if len(r.TerraformRoots) > 0 || len(r.TerraformModules) > 0 {
		c.Terraform.Binary = "terraform"
		c.Checks.Enabled = append(c.Checks.Enabled, "fmt", "validate")
	}
	for _, d := range r.TerraformRoots {
		c.Terraform.Roots = append(c.Terraform.Roots, rootFor(d))
	}
	sort.SliceStable(c.Terraform.Roots, func(i, j int) bool {
		a, b := c.Terraform.Roots[i], c.Terraform.Roots[j]
		ra, rb := workspace.EnvRank(a.Env), workspace.EnvRank(b.Env)
		return ra < rb || (ra == rb && a.Path < b.Path)
	})
	c.Terraform.Modules = moduleGlobs(r.TerraformModules)
	for _, p := range r.Ansible {
		ap := AnsibleProject{Path: p.Path}
		if p.Config != "" {
			ap.Config = relTo(p.Path, p.Config)
		}
		for _, inv := range p.Inventories {
			if env := workspace.EnvForInventory(inv); env != "" {
				if ap.Inventories == nil {
					ap.Inventories = map[string]string{}
				}
				if _, dup := ap.Inventories[env]; !dup {
					ap.Inventories[env] = relTo(p.Path, inv)
				}
			}
		}
		c.Ansible.Projects = append(c.Ansible.Projects, ap)
	}
	if len(r.Ansible) > 0 {
		c.Checks.Enabled = append(c.Checks.Enabled, "ansible-lint", "yamllint", "syntax-check")
	}
	if len(c.Checks.Enabled) > 0 {
		c.Checks.Enabled = append(c.Checks.Enabled, "secrets") // built in, needs no tool
	}
	return c
}

func rootFor(d workspace.TFDir) TFRoot {
	if env := workspace.EnvForTFRoot(d.Path); env != "" {
		return TFRoot{Path: d.Path, Env: env}
	}
	// Workspace style: env-named tfvars files map to workspaces.
	envs := map[string]TFEnv{}
	for _, f := range d.Tfvars {
		name := path.Base(f)
		name = strings.TrimSuffix(strings.TrimSuffix(name, ".tfvars"), ".auto")
		if name == "terraform" {
			continue
		}
		env := workspace.NormalizeEnv(name)
		envs[env] = TFEnv{Workspace: env, VarFiles: []string{relTo(d.Path, f)}}
	}
	if len(envs) >= 2 {
		return TFRoot{Path: d.Path, Envs: envs}
	}
	// Single-environment root: name it after the directory.
	env := path.Base(d.Path)
	if d.Path == "." || env == "terraform" || env == "." {
		env = "default"
	}
	return TFRoot{Path: d.Path, Env: workspace.NormalizeEnv(env)}
}

// moduleGlobs collapses sibling modules into parent/* patterns.
func moduleGlobs(mods []workspace.TFDir) []string {
	byParent := map[string][]string{}
	for _, m := range mods {
		p := path.Dir(m.Path)
		byParent[p] = append(byParent[p], m.Path)
	}
	var out []string
	for parent, ms := range byParent {
		if len(ms) >= 2 || path.Base(parent) == "modules" {
			out = append(out, parent+"/*")
		} else {
			out = append(out, ms...)
		}
	}
	sort.Strings(out)
	return out
}

// relTo makes file relative to dir (file is repo-relative).
func relTo(dir, file string) string {
	if dir == "." {
		return file
	}
	return strings.TrimPrefix(file, dir+"/")
}

// Selection is what the Detect screen lets the user change before writing.
type Selection struct {
	Exclude      []string `json:"exclude"`       // repo-relative paths to leave unmanaged
	ProdApproval bool     `json:"prod_approval"` // record approval policy for prod-like envs
}

// Apply returns a copy of c with excluded paths removed and the approval
// policy written out explicitly for prod-like environments.
func (c *Config) Apply(sel Selection) *Config {
	out := *c
	skip := map[string]bool{}
	for _, p := range sel.Exclude {
		skip[p] = true
	}
	out.Terraform.Roots = nil
	for _, r := range c.Terraform.Roots {
		if skip[r.Path] {
			continue
		}
		r2 := r
		if len(r.Envs) > 0 {
			r2.Envs = map[string]TFEnv{}
			for name, e := range r.Envs {
				if IsProdLike(name) {
					e.Approval = approvalValue(sel.ProdApproval)
				}
				r2.Envs[name] = e
			}
		} else if IsProdLike(r.Env) {
			r2.Approval = approvalValue(sel.ProdApproval)
		}
		out.Terraform.Roots = append(out.Terraform.Roots, r2)
	}
	out.Terraform.Modules = nil
	for _, m := range c.Terraform.Modules {
		if !skip[strings.TrimSuffix(m, "/*")] && !skip[m] {
			out.Terraform.Modules = append(out.Terraform.Modules, m)
		}
	}
	out.Ansible.Projects = nil
	for _, p := range c.Ansible.Projects {
		if !skip[p.Path] {
			out.Ansible.Projects = append(out.Ansible.Projects, p)
		}
	}
	return &out
}

func approvalValue(on bool) string {
	if on {
		return "required"
	}
	return "none"
}
