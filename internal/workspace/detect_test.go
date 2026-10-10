package workspace

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

var update = flag.Bool("update", false, "rewrite golden files")

func TestDetectGolden(t *testing.T) {
	repos, _ := filepath.Glob("../../testdata/repos/*")
	if len(repos) == 0 {
		t.Fatal("no fixtures")
	}
	for _, repo := range repos {
		name := filepath.Base(repo)
		t.Run(name, func(t *testing.T) {
			r, err := Detect(repo, nil)
			if err != nil {
				t.Fatal(err)
			}
			r.Root = ""
			got, _ := json.MarshalIndent(r, "", "  ")
			got = append(got, '\n')
			golden := filepath.Join("../../testdata/golden", name+".json")
			if *update {
				os.MkdirAll(filepath.Dir(golden), 0o755)
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("missing golden (run with -update): %v", err)
			}
			if string(want) != string(got) {
				t.Errorf("report differs from %s (run with -update to accept)\n%s", golden, got)
			}
		})
	}
}

func TestEmptyRepo(t *testing.T) {
	r, err := Detect(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Empty || r.HasIaC() {
		t.Fatalf("%+v", r)
	}
}

func TestIgnoreRespected(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "vendor/x"), 0o755)
	os.WriteFile(filepath.Join(dir, "vendor/x/main.tf"), []byte(`provider "aws" {}`), 0o644)
	os.MkdirAll(filepath.Join(dir, "skipme"), 0o755)
	os.WriteFile(filepath.Join(dir, "skipme/main.tf"), []byte(`provider "aws" {}`), 0o644)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("skipme/\n"), 0o644)
	r, _ := Detect(dir, nil)
	if r.HasIaC() {
		t.Fatalf("expected nothing, got %+v", r.TerraformRoots)
	}
}

// One dir per environment under environments/, each inventory naming its
// own top-level groups instead of `all:` (the layout of a real project).
func TestEnvironmentsDirInventories(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"ansible.cfg":                                  "[defaults]\nroles_path = ./roles\n",
		"site.yml":                                     "- hosts: p2p\n  roles: [base]\n",
		"roles/base/tasks/main.yml":                    "- ansible.builtin.ping:\n",
		"environments/dev/hosts.yml":                   "p2p:\n  children:\n    dev:\n      hosts:\n        p2p-dev:\n          ansible_host: 192.0.2.10\nlocal:\n  hosts: {}\n",
		"environments/dev/group_vars/dev/main.yml":     "deploy_env: dev\n",
		"environments/local/hosts.yml":                 "local:\n  hosts:\n    p2p-local:\n      ansible_connection: local\np2p:\n  hosts: {}\n",
		"environments/local/group_vars/local/main.yml": "deploy_env: local\n",
		"environments/local/docker-compose.yml":        "services:\n  db:\n    image: mariadb\n",
		"terraform/dev/main.tf":                        "provider \"vultr\" {}\nresource \"vultr_instance\" \"dev\" {}\n",
	}
	for p, c := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte(c), 0o644)
	}
	r, err := Detect(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Ansible) != 1 || r.Ansible[0].Path != "." {
		t.Fatalf("want one ansible project at ., got %+v", r.Ansible)
	}
	if inv := r.Ansible[0].Inventories; len(inv) != 2 || inv[0] != "environments/dev/hosts.yml" || inv[1] != "environments/local/hosts.yml" {
		t.Fatalf("inventories %v", inv)
	}
	got := map[string]Env{}
	for _, e := range r.Envs {
		got[e.Name] = e
	}
	if len(got) != 2 || len(got["dev"].Ansible) != 1 || len(got["dev"].Terraform) != 1 || len(got["local"].Ansible) != 1 {
		t.Fatalf("envs %+v", r.Envs)
	}
}

func TestLooksLikeInventoryYAML(t *testing.T) {
	cases := map[string]bool{
		"all:\n  hosts:\n    a:\n":                      true,
		"children:\n  web:\n":                           true,
		"p2p:\n  children:\n    dev:\n":                 true,
		"local:\n  hosts: {}\nweb:\n  hosts:\n    a:\n": true,
		"web:\n  vars:\n    x: 1\n":                     false, // no group declares hosts or children
		"local:\n  hosts: {}\n":                         true,
		"services:\n  db:\n    image: x\n":              false, // docker-compose
		"name: x\nhosts: y\n":                           false,
		"p2p:\n  hosts:\n    a:\n  tasks: []\n":         false,
		"- hosts: all\n  tasks: []\n":                   false,
	}
	for src, want := range cases {
		var doc yaml.Node
		if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
			t.Fatal(err)
		}
		if got := looksLikeInventoryYAML(doc.Content[0]); got != want {
			t.Errorf("%q → %v, want %v", src, got, want)
		}
	}
}

func TestEnvForInventoryEnvironmentsDir(t *testing.T) {
	cases := map[string]string{
		"environments/dev/hosts.yml":        "dev",
		"environments/local/hosts.yml":      "local",
		"ansible/envs/production/hosts.ini": "prod",
		"env/staging/inventory/hosts.yml":   "staging",
		"environments/hosts.yml":            "",
		"inventories/development/hosts.yml": "dev",
		"inventory/prod.yml":                "prod",
	}
	for p, want := range cases {
		if got := EnvForInventory(p); got != want {
			t.Errorf("%s → %q, want %q", p, got, want)
		}
	}
}
