package ansible

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for p, c := range files {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const topoInv = `{
 "_meta": {"hostvars": {
   "web-1": {"ansible_host": "10.0.1.10", "app_port": 9000, "metrics_ports": [9100, "9101/udp"]},
   "web-2": {"ansible_host": "10.0.1.11", "ansible_port": "{{ ssh_port }}", "ssh_port": 2222},
   "db-1": {"ansible_connection": "local", "pg_port": "{{ lookup('env', 'X') }}"},
   "staging-web": {}
 }},
 "all": {"children": ["ungrouped", "web", "db", "staging"]},
 "web": {"hosts": ["web-1", "web-2", "staging-web"]},
 "db": {"hosts": ["db-1"]},
 "staging": {"hosts": ["staging-web"]}
}`

func TestPlaysRolesAndPorts(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"playbooks/site.yml": `
- import_playbook: other.yml
- hosts: web:!staging
  roles:
    - app
    - { role: nginx, tags: [web] }
  tasks:
    - block:
        - ansible.builtin.include_role: { name: monitoring }
- hosts: [db]
  pre_tasks:
    - import_role:
        name: "{{ dynamic }}"
  roles: [postgres]
- hosts: "{{ target }}"
  roles: [ignored]
`,
		"roles/app/defaults/main.yml":        "app_port: 8080\napp_admin_port: 8081\n",
		"roles/app/meta/main.yml":            "dependencies:\n  - role: common\n",
		"roles/common/defaults/main.yml":     "unrelated: 1\n",
		"roles/nginx/vars/main.yml":          "nginx_port: 443\n",
		"roles/monitoring/defaults/main.yml": "exporter_port: \"{{ base_port }}\"\nbase_port: 9200\n",
		"playbooks/broken.yml":               "- hosts: [\n",
	})
	plays := Plays(dir, []string{"playbooks/site.yml", "playbooks/broken.yml", "playbooks/missing.yml"})
	if len(plays) != 3 || strings.Join(plays[0].Roles, ",") != "app,nginx,monitoring" || plays[1].Hosts != "db" || strings.Join(plays[1].Roles, ",") != "postgres" {
		t.Fatalf("%+v", plays)
	}
	if got := RoleClosure(dir, "playbooks/site.yml", plays[0].Roles); strings.Join(got, ",") != "common,app,nginx,monitoring" {
		t.Fatalf("dependencies come first: %v", got)
	}

	inv, err := ParseInventoryList([]byte(topoInv))
	if err != nil {
		t.Fatal(err)
	}
	match := func(p string) string {
		h, ok := inv.Match(p)
		return strings.Join(h, ",") + map[bool]string{true: "", false: "?"}[ok]
	}
	for p, want := range map[string]string{
		"web:!staging": "web-1,web-2",
		"web,db":       "db-1,staging-web,web-1,web-2",
		"all:&staging": "staging-web",
		"web-*":        "web-1,web-2",
		"~^db":         "db-1",
		"!db":          "staging-web,web-1,web-2",
		"{{ target }}": "?",
		"nope":         "",
	} {
		if got := match(p); got != want {
			t.Errorf("%q: %q, want %q", p, got, want)
		}
	}

	refs := []RoleRef{{"common", "playbooks/site.yml"}, {"app", "playbooks/site.yml"}, {"nginx", "playbooks/site.yml"}, {"monitoring", "playbooks/site.yml"}}
	ports := func(host string) string {
		var out []string
		for _, p := range DeclaredPorts(dir, inv, host, refs) {
			out = append(out, strings.Join([]string{strconv.Itoa(p.Port), p.Proto, p.Var, p.Role}, "/"))
		}
		return strings.Join(out, " ")
	}
	// inventory beats role defaults; role vars beat the inventory
	if got := ports("web-1"); got != "22/tcp/ssh (ansible_port)/ 8081/tcp/app_admin_port/app 9000/tcp/app_port/ 9200/tcp/base_port/monitoring 9200/tcp/exporter_port/monitoring 9100/tcp/metrics_ports/ 9101/udp/metrics_ports/ 443/tcp/nginx_port/nginx" {
		t.Fatalf("%s", got)
	}
	if got := ports("web-2"); !strings.HasPrefix(got, "2222/tcp/ssh") {
		t.Fatalf("ansible_port follows a simple reference: %s", got)
	}
	if got := DeclaredPorts(dir, inv, "db-1", nil); len(got) != 0 {
		t.Fatalf("local connection has no ssh, a lookup can't be resolved: %+v", got)
	}
}
