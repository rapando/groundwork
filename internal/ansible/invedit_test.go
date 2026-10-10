package ansible

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Shaped like a real repo's inventory: a comment header, named top-level
// groups instead of all:, a nested child group and an empty group.
const archInv = `---
# Dev inventory: the whole stack on one server.
#
# Committed on purpose -- it holds an address, never a password.
p2p:
  children:
    dev:
      hosts:
        p2p-dev:
          ansible_host: 139.84.236.40   # Vultr
          ansible_port: 22

local:
  hosts: {}
`

const labInv = `all:
  vars:
    ntp_server: pool.ntp.org
  children:
    web:
      hosts:
        web-1:
        web-2:
          tier: canary
    db:
      hosts:
        db-1:
`

func edit(t *testing.T, src string, e InvEdit) string {
	t.Helper()
	out, err := EditInventory([]byte(src), e)
	if err != nil {
		t.Fatalf("%+v: %v", e, err)
	}
	return string(out)
}

func TestInventoryEditsKeepTheRestOfTheFile(t *testing.T) {
	cases := []struct {
		name string
		src  string
		e    InvEdit
		want string
	}{
		{"add host to a nested group", archInv, InvEdit{Op: "add_host", Host: "p2p-dev-2", Group: "dev", Address: "10.0.0.2"},
			strings.Replace(archInv, "          ansible_port: 22\n", "          ansible_port: 22\n        p2p-dev-2:\n          ansible_host: 10.0.0.2\n", 1)},
		{"add host to an empty group", archInv, InvEdit{Op: "add_host", Host: "laptop", Group: "local"},
			strings.Replace(archInv, "  hosts: {}\n", "  hosts:\n    laptop:\n", 1)},
		{"add host to a group without hosts", labInv, InvEdit{Op: "add_host", Host: "web-3", Group: "web"},
			strings.Replace(labInv, "          tier: canary\n", "          tier: canary\n        web-3:\n", 1)},
		{"remove a host's last entry leaves hosts: {}", archInv, InvEdit{Op: "remove_host", Host: "p2p-dev", Group: "dev"},
			strings.Replace(archInv, "      hosts:\n        p2p-dev:\n          ansible_host: 139.84.236.40   # Vultr\n          ansible_port: 22\n", "      hosts: {}\n", 1)},
		{"remove one host of several", labInv, InvEdit{Op: "remove_host", Host: "web-2"},
			strings.Replace(labInv, "        web-2:\n          tier: canary\n", "", 1)},
		{"add a top-level group (no all:)", archInv, InvEdit{Op: "add_group", Group: "staging"},
			archInv + "staging: {}\n"},
		{"add a child group", archInv, InvEdit{Op: "add_group", Group: "prod", Parent: "p2p"},
			strings.Replace(archInv, "          ansible_port: 22\n", "          ansible_port: 22\n    prod: {}\n", 1)},
		{"add a group under all:", labInv, InvEdit{Op: "add_group", Group: "cache"},
			labInv + "    cache: {}\n"},
		{"remove an empty group", archInv, InvEdit{Op: "remove_group", Group: "local"},
			strings.Replace(archInv, "\nlocal:\n  hosts: {}\n", "\n", 1)},
		{"set a host var in place, keeping its comment-free neighbours", archInv, InvEdit{Op: "set_var", Scope: "host", Host: "p2p-dev", Key: "ansible_port", Value: "2222"},
			strings.Replace(archInv, "ansible_port: 22\n", "ansible_port: 2222\n", 1)},
		{"add a var to a host written as host:", labInv, InvEdit{Op: "set_var", Scope: "host", Host: "web-1", Key: "tier", Value: "stable"},
			strings.Replace(labInv, "        web-1:\n", "        web-1:\n          tier: stable\n", 1)},
		{"unset a host's only var leaves host:", labInv, InvEdit{Op: "unset_var", Scope: "host", Host: "web-2", Key: "tier"},
			strings.Replace(labInv, "        web-2:\n          tier: canary\n", "        web-2:\n", 1)},
		{"add group vars", archInv, InvEdit{Op: "set_var", Scope: "group", Group: "local", Key: "deploy_env", Value: "local"},
			strings.Replace(archInv, "  hosts: {}\n", "  hosts: {}\n  vars:\n    deploy_env: local\n", 1)},
		{"set an existing group var", labInv, InvEdit{Op: "set_var", Scope: "group", Group: "all", Key: "ntp_server", Value: `"time.example.com"`},
			strings.Replace(labInv, "ntp_server: pool.ntp.org", `ntp_server: "time.example.com"`, 1)},
		{"unset a group's only var leaves vars: {}", labInv, InvEdit{Op: "unset_var", Scope: "group", Group: "all", Key: "ntp_server"},
			strings.Replace(labInv, "  vars:\n    ntp_server: pool.ntp.org\n", "  vars: {}\n", 1)},
		{"vars on all: when the file has no all:", archInv, InvEdit{Op: "set_var", Scope: "group", Group: "all", Key: "tz", Value: "UTC"},
			archInv + "all:\n  vars:\n    tz: UTC\n"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := edit(t, c.src, c.e); got != c.want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, c.want)
			}
		})
	}
}

func TestInventoryEditsRefuseWhatTheyCantDoSafely(t *testing.T) {
	cases := []struct {
		src  string
		e    InvEdit
		want string
	}{
		{archInv, InvEdit{Op: "add_host", Host: "x", Group: "nope"}, "no group nope"},
		{archInv, InvEdit{Op: "add_host", Host: "p2p-dev", Group: "dev"}, "already in dev"},
		{archInv, InvEdit{Op: "add_host", Host: "bad host", Group: "dev"}, "invalid host name"},
		{archInv, InvEdit{Op: "add_host", Host: "../x", Group: "dev"}, "invalid host name"},
		{archInv, InvEdit{Op: "add_group", Group: "dev"}, "already exists"},
		{archInv, InvEdit{Op: "add_group", Group: "a-b"}, "invalid group name"},
		{archInv, InvEdit{Op: "remove_group", Group: "p2p"}, "still has child groups"},
		{archInv, InvEdit{Op: "remove_group", Group: "all"}, "can't be removed"},
		{archInv, InvEdit{Op: "remove_host", Host: "ghost"}, "isn't listed"},
		{archInv, InvEdit{Op: "set_var", Scope: "host", Host: "p2p-dev", Key: "x", Value: "a: b"}, "not a valid YAML value"},
		{archInv, InvEdit{Op: "set_var", Scope: "host", Host: "p2p-dev", Key: "x", Value: "[1"}, "not a valid YAML value"},
		{archInv, InvEdit{Op: "set_var", Scope: "host", Host: "p2p-dev", Key: "x", Value: "a\nb: c"}, "one line"},
		{archInv, InvEdit{Op: "set_var", Scope: "host", Host: "p2p-dev", Key: "bad-key", Value: "1"}, "invalid variable name"},
		{"web: {hosts: {a: }}\n", InvEdit{Op: "add_host", Host: "b", Group: "web"}, "flow style"},
		{"- not\n- an inventory\n", InvEdit{Op: "add_group", Group: "x"}, "isn't a YAML inventory"},
	}
	for _, c := range cases {
		if _, err := EditInventory([]byte(c.src), c.e); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: want error containing %q, got %v", c.e, c.want, err)
		}
	}
}

func TestVarsFileEdits(t *testing.T) {
	v := func(s string) *string { return &s }
	src := "---\n# web tier\nnginx_workers: 3   # per core\nlog_level: info\n"
	cases := []struct {
		src   string
		key   string
		value *string
		want  string
	}{
		{src, "nginx_workers", v("4"), "---\n# web tier\nnginx_workers: 4\nlog_level: info\n"},
		{src, "tz", v("UTC"), src + "tz: UTC\n"},
		{src, "log_level", nil, "---\n# web tier\nnginx_workers: 3   # per core\n"},
		{"", "tz", v("UTC"), "---\ntz: UTC\n"},
		{"---\n# nothing yet\n", "tz", v("UTC"), "---\n# nothing yet\ntz: UTC\n"},
	}
	for _, c := range cases {
		got, err := EditVarsFile([]byte(c.src), c.key, c.value)
		if err != nil || string(got) != c.want {
			t.Errorf("%q %s: got %q (%v), want %q", c.src, c.key, got, err, c.want)
		}
	}
	if _, err := EditVarsFile([]byte("$ANSIBLE_VAULT;1.1;AES256\n6162\n"), "x", v("1")); err == nil || !strings.Contains(err.Error(), "vault-encrypted") {
		t.Fatalf("vault file: %v", err)
	}
	if _, err := EditVarsFile([]byte(src), "ghost", nil); err == nil {
		t.Fatal("unsetting a missing key must fail")
	}
}

// Ansible itself must read every edited inventory the way the edit intends.
func TestEditedInventoriesParseInAnsible(t *testing.T) {
	if _, err := exec.LookPath("ansible-inventory"); err != nil {
		t.Skip("ansible not installed")
	}
	steps := []InvEdit{
		{Op: "add_group", Group: "prod", Parent: "p2p"},
		{Op: "add_host", Host: "prod-1", Group: "prod", Address: "10.0.0.9"},
		{Op: "add_host", Host: "laptop", Group: "local"},
		{Op: "set_var", Scope: "group", Group: "prod", Key: "deploy_env", Value: "prod"},
		{Op: "set_var", Scope: "host", Host: "laptop", Key: "ansible_connection", Value: "local"},
		{Op: "remove_host", Host: "p2p-dev"},
		{Op: "unset_var", Scope: "host", Host: "laptop", Key: "ansible_connection"},
		{Op: "add_group", Group: "empty"},
		{Op: "remove_group", Group: "empty"},
	}
	src := archInv
	for _, s := range steps {
		src = edit(t, src, s)
	}
	f := filepath.Join(t.TempDir(), "hosts.yml")
	os.WriteFile(f, []byte(src), 0o644)
	out, err := exec.Command("ansible-inventory", "-i", f, "--list").Output()
	if err != nil {
		t.Fatalf("%v\n%s", err, src)
	}
	var inv map[string]struct {
		Hosts    []string `json:"hosts"`
		Children []string `json:"children"`
	}
	json.Unmarshal(out, &inv)
	meta := struct {
		Meta struct {
			Hostvars map[string]map[string]any `json:"hostvars"`
		} `json:"_meta"`
	}{}
	json.Unmarshal(out, &meta)
	if h := inv["prod"].Hosts; len(h) != 1 || h[0] != "prod-1" {
		t.Fatalf("prod: %v\n%s", inv["prod"], src)
	}
	if h := inv["local"].Hosts; len(h) != 1 || h[0] != "laptop" {
		t.Fatalf("local: %v\n%s", inv["local"], src)
	}
	if hv := meta.Meta.Hostvars["prod-1"]; hv["ansible_host"] != "10.0.0.9" || hv["deploy_env"] != "prod" {
		t.Fatalf("prod-1 vars: %v\n%s", hv, src)
	}
	if _, ok := meta.Meta.Hostvars["p2p-dev"]; ok {
		t.Fatalf("p2p-dev should be gone\n%s", src)
	}
	if _, ok := inv["empty"]; ok {
		t.Fatalf("empty group should be gone\n%s", src)
	}
	if !strings.Contains(src, "# Committed on purpose -- it holds an address, never a password.") {
		t.Fatal("the header comment was lost")
	}
}

func TestInventoryGroupsIncludesEmptyOnes(t *testing.T) {
	var got []string
	for _, g := range InventoryGroups([]byte(archInv)) {
		got = append(got, fmt.Sprintf("%s@%d", g.Name, g.Depth))
	}
	if strings.Join(got, ",") != "p2p@1,dev@2,local@1" {
		t.Fatalf("%v", got)
	}
	got = nil
	for _, g := range InventoryGroups([]byte(labInv)) {
		got = append(got, fmt.Sprintf("%s@%d", g.Name, g.Depth))
	}
	if strings.Join(got, ",") != "web@1,db@1" {
		t.Fatalf("%v", got)
	}
}
