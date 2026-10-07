package workspace

import (
	"path"
	"sort"
	"strings"
)

var envAliases = map[string]string{
	"production": "prod", "prd": "prod",
	"stg": "staging", "stage": "staging",
	"development": "dev",
}

// NormalizeEnv maps common spellings onto one canonical name.
func NormalizeEnv(n string) string {
	n = strings.ToLower(strings.TrimSpace(n))
	if a, ok := envAliases[n]; ok {
		return a
	}
	return n
}

func envFromTfvars(file string) string {
	b := path.Base(file)
	b = strings.TrimSuffix(b, ".tfvars")
	b = strings.TrimSuffix(b, ".auto") // dev.auto.tfvars
	if b == "terraform" || b == "" || strings.HasSuffix(file, ".auto.tfvars") && b == "" {
		return ""
	}
	return NormalizeEnv(b)
}

// EnvForTFRoot infers the environment of a dir-per-env root from its path.
func EnvForTFRoot(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if (s == "envs" || s == "environments" || s == "env") && i+1 < len(segs) {
			return NormalizeEnv(segs[i+1])
		}
	}
	return ""
}

// EnvForInventory infers the environment from an inventory path
// (inventory/dev.yml, inventory/prod/hosts.yml, inventories/staging).
func EnvForInventory(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if (s == "inventory" || s == "inventories") && i+1 < len(segs)-0 {
			next := segs[i+1]
			if i+1 == len(segs)-1 { // a file directly in inventory/
				ext := path.Ext(next)
				name := strings.TrimSuffix(next, ext)
				// a script is a dynamic inventory spanning environments, not one environment
				if ext == ".py" || name == "hosts" || name == "inventory" || name == "all" {
					return ""
				}
				return NormalizeEnv(name)
			}
			return NormalizeEnv(next)
		}
	}
	return ""
}

func buildEnvs(roots []TFDir, projects []AnsibleProject) []Env {
	m := map[string]*Env{}
	get := func(n string) *Env {
		if m[n] == nil {
			m[n] = &Env{Name: n}
		}
		return m[n]
	}
	for _, r := range roots {
		if e := EnvForTFRoot(r.Path); e != "" {
			get(e).Terraform = append(get(e).Terraform, r.Path)
			continue
		}
		for _, f := range r.Tfvars {
			if e := envFromTfvars(f); e != "" {
				get(e).Terraform = append(get(e).Terraform, r.Path+"#"+e)
			}
		}
	}
	for _, p := range projects {
		for _, i := range p.Inventories {
			if e := EnvForInventory(i); e != "" {
				env := get(e)
				if !contains(env.Ansible, i) {
					env.Ansible = append(env.Ansible, i)
				}
			}
		}
	}
	out := make([]Env, 0, len(m))
	for _, e := range m {
		sort.Strings(e.Terraform)
		sort.Strings(e.Ansible)
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		return EnvRank(out[i].Name) < EnvRank(out[j].Name) || (EnvRank(out[i].Name) == EnvRank(out[j].Name) && out[i].Name < out[j].Name)
	})
	return out
}

// EnvRank orders environments dev < test < staging < prod < others.
func EnvRank(n string) int {
	switch n {
	case "dev":
		return 0
	case "test", "qa":
		return 1
	case "staging":
		return 2
	case "prod":
		return 3
	}
	return 4
}
