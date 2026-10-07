package ansible

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestGroupVarsMatrix(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "ansible")
	write(t, proj, "inventories/dev/hosts.yml", "all:\n  children:\n    web:\n      vars:\n        nginx_workers: 1\n      hosts:\n        w1: {}\n")
	write(t, proj, "inventories/dev/group_vars/all.yml", "app_port: 8000\ndb_password: dev-plain-pw\n")
	write(t, proj, "inventories/prod/hosts.yml", "all:\n  children:\n    web:\n      hosts:\n        w1: {}\n")
	write(t, proj, "inventories/prod/group_vars/all.yml", "app_port: 8000\ndb_password: !vault |\n  $ANSIBLE_VAULT;1.1;AES256\n  6161\n")
	write(t, proj, "inventories/prod/group_vars/web.yml", "nginx_workers: 4\n")
	write(t, proj, "inventories/prod/group_vars/secrets.yml", "$ANSIBLE_VAULT;1.1;AES256\n6161\n")
	write(t, proj, "group_vars/web.yml", "# playbook-level\nlisten: 80\n")

	gm := GroupVarsMatrix(root, proj, map[string]string{
		"dev":  filepath.Join(proj, "inventories/dev/hosts.yml"),
		"prod": filepath.Join(proj, "inventories/prod"),
	})
	if len(gm.Envs) != 2 || gm.Envs[0] != "dev" {
		t.Fatalf("envs %v", gm.Envs)
	}
	get := func(g, n string) MatrixRow {
		for _, r := range gm.Rows {
			if r.Group == g && r.Name == n {
				return r
			}
		}
		t.Fatalf("no row %s/%s in %+v", g, n, gm.Rows)
		return MatrixRow{}
	}
	if r := get("all", "app_port"); r.Differs || r.Cells["dev"].Value != "8000" || r.Cells["dev"].Line != 1 {
		t.Errorf("app_port %+v", r)
	}
	nw := get("web", "nginx_workers")
	if !nw.Differs || nw.Cells["dev"].Source != "inventory file" || nw.Cells["prod"].Value != "4" || nw.Cells["prod"].File != "ansible/inventories/prod/group_vars/web.yml" {
		t.Errorf("nginx_workers %+v", nw)
	}
	pw := get("all", "db_password")
	if !pw.Secret || pw.Cells["dev"].Value != "••••••" || pw.Cells["prod"].State != "vault" || pw.Cells["dev"].Line != 2 {
		t.Errorf("db_password %+v", pw)
	}
	if l := get("web", "listen"); l.Cells["dev"].Source != "playbook group_vars/web" || l.Missing {
		t.Errorf("listen %+v", l)
	}
	if len(gm.EncryptedFiles["prod"]) != 1 {
		t.Errorf("encrypted %v", gm.EncryptedFiles)
	}
	if gm.Rows[0].Group != "all" {
		t.Errorf("all first: %v", gm.Rows[0])
	}
}
