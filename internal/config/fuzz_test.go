package config

import (
	"strings"
	"testing"
)

func FuzzParse(f *testing.F) {
	f.Add([]byte("version: 1\nmode: standalone\nterraform:\n  roots:\n    - path: envs/dev\n      env: dev\n"))
	f.Add([]byte("version: 1\ndrift: { schedule: '0 * * * *', notify: webhook }\n"))
	f.Add([]byte("version: 1\ndrift: { notify: webhook, webhook: 'https://user:pw@h/x' }\nignore: ['', 'a\\nb']\n"))
	f.Add([]byte("version: 1\nterraform:\n  roots:\n    - path: a\n      envs: { dev: { workspace: '-chdir=/' }, 'de v': {} }\n"))
	f.Add([]byte("version: 1\nansible:\n  projects:\n    - path: a\n      config: ../ansible.cfg\n      vault_password_file: ~root/../x\n      inventories: { 'd;v': ../inv }\n"))
	f.Fuzz(func(t *testing.T, b []byte) {
		c, err := Parse(b, "groundwork.yaml")
		if err != nil {
			return
		}
		// whatever parsed must satisfy the rules that protect subprocess arguments
		for _, r := range c.Terraform.Roots {
			if r.Env != "" && !ValidEnvName(r.Env) {
				t.Fatalf("env %q accepted", r.Env)
			}
			for name, e := range r.Envs {
				if !ValidEnvName(name) || (e.Workspace != "" && !ValidEnvName(e.Workspace)) {
					t.Fatalf("envs %q/%q accepted", name, e.Workspace)
				}
			}
		}
		for _, p := range c.Ansible.Projects {
			if strings.Contains(p.VaultPasswordFile, "..") || strings.Contains(p.Config, "..") {
				t.Fatalf("path with .. accepted: %+v", p)
			}
		}
		if w := c.Drift.Webhook; w != "" && !strings.HasPrefix(w, "http://") && !strings.HasPrefix(w, "https://") {
			t.Fatalf("webhook %q accepted", w)
		}
	})
}
