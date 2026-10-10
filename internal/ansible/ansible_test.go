package ansible

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/ansible/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func events(t *testing.T, name string) []Event {
	t.Helper()
	var out []Event
	sc := bufio.NewScanner(strings.NewReader(string(fixture(t, name))))
	for sc.Scan() {
		e, ok := ParseEvent(sc.Bytes())
		if !ok {
			t.Fatalf("unparsed real event: %s", sc.Text())
		}
		out = append(out, e)
	}
	return out
}

func TestParseRealPlaybookEvents(t *testing.T) {
	evs := events(t, "events_run.ndjson")
	byHostTask := map[string]*HostResult{}
	var stats map[string]HostStats
	for _, e := range evs {
		if e.Result != nil {
			byHostTask[e.Result.Host+"|"+e.Result.Task] = e.Result
		}
		if e.Type == "stats" {
			stats = e.Stats
		}
	}
	if r := byHostTask["web-1|web : Write config"]; r == nil || r.Status != "changed" || !r.Changed || !strings.Contains(r.Diff, "workers=4") {
		t.Fatalf("changed with diff: %+v", r)
	}
	if r := byHostTask["web-2|Fail on canary"]; r == nil || r.Status != "failed" || r.Msg != "canary refuses" {
		t.Fatalf("failure: %+v", r)
	}
	if r := byHostTask["web-1|Fail on canary"]; r == nil || r.Status != "skipped" {
		t.Fatalf("skipped: %+v", r)
	}
	if r := byHostTask["down-1|Ping"]; r == nil || r.Status != "unreachable" || !strings.Contains(r.Msg, "Connection refused") {
		t.Fatalf("unreachable: %+v", r)
	}
	if s := stats["web-2"]; s.Failures != 1 || s.Changed != 1 || stats["down-1"].Unreachable != 1 {
		t.Fatalf("stats %+v", stats)
	}
}

func TestCheckModeEventsAndFacts(t *testing.T) {
	for _, e := range events(t, "events_check.ndjson") {
		if e.Result != nil && e.Result.Task == "web : Write config" && (e.Result.Status != "changed" || e.Result.Diff == "") {
			t.Fatalf("a dry run still reports what would change: %+v", e.Result)
		}
	}
	var facts map[string]any
	for _, e := range events(t, "events_setup.ndjson") {
		if e.Result != nil && e.Result.Host == "web-1" {
			facts = e.Result.Facts
		}
	}
	if facts["ansible_os_family"] != "Darwin" || facts["ansible_python_version"] == nil {
		t.Fatalf("%v", facts)
	}
	if _, leaked := facts["ansible_env"]; leaked {
		t.Fatal("ansible_env must never be kept (it carries credentials)")
	}
}

func TestParseInventoryList(t *testing.T) {
	inv, err := ParseInventoryList(fixture(t, "inventory_list.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(inv.Hosts, ",") != "db-1,down-1,web-1,web-2" {
		t.Fatalf("%v", inv.Hosts)
	}
	if g := inv.Groups["web"]; g.Depth != 1 || g.Total != 2 || inv.Groups["all"].Total != 4 {
		t.Fatalf("%+v %+v", g, inv.Groups["all"])
	}
	var names []string
	for _, g := range inv.GroupsOf("web-2") {
		names = append(names, g.Name)
	}
	if strings.Join(names, ",") != "all,web" {
		t.Fatalf("%v", names)
	}
	if inv.Address("down-1") != "127.0.0.1" || inv.Address("web-1") != "local" {
		t.Fatal("addresses")
	}
}

func TestProvenanceMatchesRealAnsible(t *testing.T) {
	root, _ := filepath.Abs("../../testdata/repos")
	proj := filepath.Join(root, "ansible-lab")
	inv, _ := ParseInventoryList(fixture(t, "inventory_list.json"))
	var actual map[string]any
	json.Unmarshal(fixture(t, "inventory_host_web-2.json"), &actual)

	hv := Provenance(root, proj, filepath.Join(proj, "inventory/dev.yml"), inv, "web-2", actual)
	rows := map[string]VarRow{}
	for _, r := range hv.Rows {
		rows[r.Name] = r
	}
	nw := rows["nginx_workers"]
	// real ansible says web-2 gets 3 (playbook group_vars beats inventory group_vars)
	if nw.Status != "ok" || nw.Value != "3" || nw.Winner.File != "ansible-lab/group_vars/web.yml" || nw.Winner.Level != "playbook group_vars/web" {
		t.Fatalf("%+v", nw)
	}
	var over []string
	for _, o := range nw.Overridden {
		over = append(over, o.Level+"="+o.Value)
	}
	if strings.Join(over, " | ") != "inventory group_vars/web=2 | inventory file, group web=1 | role default (web)=auto" {
		t.Fatalf("override chain: %v", over)
	}
	if r := rows["app_port"]; r.Value != "8080" || r.Winner.Level != "inventory group_vars/all" || r.Overridden[0].Value != "8000" {
		t.Fatalf("%+v", r)
	}
	if r := rows["tier"]; r.Status != "ok" || r.Winner.Level != "inventory file, host" {
		t.Fatalf("%+v", r)
	}
	if r := rows["log_level"]; r.Value != "info" || r.Overridden[0].Level != "role default (web)" {
		t.Fatalf("%+v", r)
	}
	// connection settings sort last
	if !strings.HasPrefix(hv.Rows[len(hv.Rows)-1].Name, "ansible_") {
		t.Fatalf("order: %v", hv.Rows)
	}
}

func TestProvenanceAdmitsWhatItCannotPlace(t *testing.T) {
	root, _ := filepath.Abs("../../testdata/repos")
	proj := filepath.Join(root, "ansible-lab")
	inv, _ := ParseInventoryList(fixture(t, "inventory_list.json"))
	actual := map[string]any{"nginx_workers": 99, "from_plugin": "x"} // ansible disagrees / knows more
	hv := Provenance(root, proj, filepath.Join(proj, "inventory/dev.yml"), inv, "web-2", actual)
	for _, r := range hv.Rows {
		switch r.Name {
		case "nginx_workers":
			if r.Status != "unknown" || r.Value != "99" {
				t.Fatalf("on disagreement show ansible's value, not a guessed source: %+v", r)
			}
		case "from_plugin":
			if r.Status != "unknown" || r.Winner != nil {
				t.Fatalf("%+v", r)
			}
		}
	}
}

func TestProvenanceMasksSecretsAndVault(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "ops")
	write := func(p, s string) {
		os.MkdirAll(filepath.Dir(filepath.Join(proj, p)), 0o755)
		os.WriteFile(filepath.Join(proj, p), []byte(s), 0o644)
	}
	write("hosts.ini", "[web]\nweb-1 ansible_host=10.0.0.5 db_password=hunter2\n\n[web:vars]\nport=81\n")
	write("group_vars/web/main.yml", "port: 82\napi_token: abc123\n")
	write("group_vars/web/vault.yml", "$ANSIBLE_VAULT;1.1;AES256\n6162636465\n")
	write("host_vars/web-1.yml", "tls_key: !vault |\n  $ANSIBLE_VAULT;1.1;AES256\n  616263\n")
	inv := &Inventory{Groups: map[string]*Group{"all": {Name: "all", Children: []string{"web"}}, "web": {Name: "web", Hosts: []string{"web-1"}, Depth: 1}}, Hosts: []string{"web-1"}}
	hv := Provenance(root, proj, filepath.Join(proj, "hosts.ini"), inv, "web-1", nil)
	b, _ := json.Marshal(hv)
	for _, leak := range []string{"hunter2", "abc123", "616263"} {
		if strings.Contains(string(b), leak) {
			t.Fatalf("leaked %q: %s", leak, b)
		}
	}
	rows := map[string]VarRow{}
	for _, r := range hv.Rows {
		rows[r.Name] = r
	}
	if rows["port"].Value != "82" || rows["port"].Overridden[0].Value != "81" {
		t.Fatalf("INI group vars and directory-form group_vars: %+v", rows["port"])
	}
	if rows["tls_key"].Value != "(vault encrypted)" || rows["db_password"].Value != "••••" {
		t.Fatalf("%+v %+v", rows["tls_key"], rows["db_password"])
	}
	if len(hv.EncryptedFiles) != 1 || hv.EncryptedFiles[0] != "ops/group_vars/web/vault.yml" {
		t.Fatalf("whole-file vault: %v", hv.EncryptedFiles)
	}
}

func TestArgvSafety(t *testing.T) {
	a := PlaybookArgs(PlaybookOpts{Inventory: "inventory/prod.yml", Playbook: "site.yml", Check: true, Limit: "web:&prod", Tags: "nginx"})
	want := "ansible-playbook -i inventory/prod.yml --diff --check --limit=web:&prod --tags=nginx -- site.yml"
	if strings.Join(a, " ") != want {
		t.Fatalf("%s", strings.Join(a, " "))
	}
	if l := InventoryListArgs("inventory/dev.yml", "/p", "/k/pw"); strings.Join(l, " ") != "ansible-inventory -i inventory/dev.yml --vault-password-file=/k/pw --list --playbook-dir=/p" {
		t.Fatalf("%v", l)
	}
	if l := InventoryHostArgs("", "/p", "web-1", ""); strings.Join(l, " ") != "ansible-inventory --host=web-1 --playbook-dir=/p" {
		t.Fatalf("%v", l)
	}
	ad := AdhocArgs("", "web", "ping", "", false, "")
	if strings.Join(ad, " ") != "ansible -m ping -- web" {
		t.Fatalf("%v", ad)
	}
	for _, bad := range []string{"", "-v", "--version", "web; rm -rf /", "a b", "$(x)", "web`x`"} {
		if ValidPattern(bad) {
			t.Errorf("pattern %q accepted", bad)
		}
	}
	for _, ok := range []string{"all", "web:&prod", "web-*", "!db", "web[0:2]", "host.example.com"} {
		if !ValidPattern(ok) {
			t.Errorf("pattern %q rejected", ok)
		}
	}
	if ValidModule("shell; ls") || !ValidModule("ansible.builtin.ping") {
		t.Fatal("module validation")
	}
	if DefaultInventory("../../testdata/repos/ansible-lab/ansible.cfg") != "inventory/dev.yml" {
		t.Fatal("ansible.cfg inventory")
	}
}

func TestInstallCallback(t *testing.T) {
	dir := t.TempDir()
	env, err := InstallCallback(dir)
	if err != nil || len(env) != 4 {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(dir, "groundwork.py"))
	if !strings.Contains(string(b), "CALLBACK_NAME = \"groundwork\"") {
		t.Fatal("callback not written")
	}
}
