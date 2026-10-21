package api

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rapando/groundwork/internal/events"
	"github.com/rapando/groundwork/internal/store"
)

func TestGraphAndArchitectureOverHTTP(t *testing.T) {
	e := newConfigured(t) // iac-only fixture: envs/dev, envs/prod, modules/network
	g := into(t, call(t, e.h, "GET", "/graph/deps?root=terraform/envs/dev&env=dev", nil))["graph"].(map[string]any)
	ids := map[string]bool{}
	for _, n := range g["nodes"].([]any) {
		ids[n.(map[string]any)["id"].(string)] = true
	}
	if !ids["module.network.aws_vpc.main"] || !ids["module.network.aws_subnet.private"] || g["source"] != "static" {
		t.Fatalf("%v %v", ids, g["source"])
	}
	if call(t, e.h, "GET", "/graph/deps?root=nope&env=dev", nil).Code != 400 {
		t.Fatal("bad root")
	}

	// unsaved buffer changes the architecture without touching disk
	mod := "terraform/modules/network/main.tf"
	orig, _ := os.ReadFile(filepath.Join(e.root, mod))
	arch := func(buf string) map[string]any {
		body := map[string]any{"root": "terraform/envs/dev", "env": "dev"}
		if buf != "" {
			body["buffers"] = map[string]string{mod: buf}
		}
		return into(t, call(t, e.h, "POST", "/graph/architecture", body))["architecture"].(map[string]any)
	}
	a := arch("")
	if c := a["containers"].([]any); len(c) != 1 || !strings.Contains(strings.Join(toStrings(c[0].(map[string]any)["children"].([]any)), ","), "aws_subnet.private") {
		t.Fatalf("%v", a["containers"])
	}
	edited := strings.Replace(string(orig), "  vpc_id     = aws_vpc.main.id\n", "", 1)
	a = arch(edited)
	if kids := a["containers"].([]any)[0].(map[string]any)["children"].([]any); len(kids) != 0 {
		t.Fatalf("the subnet no longer references the VPC in the buffer: %v", kids)
	}
	if b, _ := os.ReadFile(filepath.Join(e.root, mod)); string(b) != string(orig) {
		t.Fatal("architecture must never write")
	}
	if rec := call(t, e.h, "POST", "/graph/architecture", map[string]any{"root": "terraform/envs/dev", "env": "dev", "buffers": map[string]string{"../x.tf": "x"}}); rec.Code != 400 {
		t.Fatalf("buffer paths go through SafePath: %d", rec.Code)
	}
	roots := into(t, call(t, e.h, "GET", "/graph/roots?file="+mod, nil))["roots"].([]any)
	if len(roots) != 2 {
		t.Fatalf("the module is used by dev and prod: %v", roots)
	}
}

func toStrings(l []any) []string {
	out := make([]string, len(l))
	for i, x := range l {
		out[i] = x.(string)
	}
	return out
}

func TestDriftOverHTTPWithActions(t *testing.T) {
	withFakeTerraform(t)
	e := newConfigured(t)
	// make the drifted resource exist in code so copy/ignore can locate it
	os.WriteFile(filepath.Join(e.root, "terraform/envs/dev/files.tf"), []byte("resource \"local_file\" \"motd\" {\n  filename        = \"motd.txt\"\n  content         = \"hello\"\n  file_permission = \"0644\"\n}\n"), 0o644)

	rec := call(t, e.h, "POST", "/runs", map[string]any{"kind": "tf.drift", "root": "terraform/envs/dev", "env": "dev"})
	if rec.Code != 202 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	waitRun(t, e, into(t, rec)["run"].(map[string]any)["id"].(float64), "succeeded")

	res := into(t, call(t, e.h, "GET", "/drift?root=terraform/envs/dev&env=dev", nil))["resources"].([]any)
	if len(res) != 1 {
		t.Fatalf("%v", res)
	}
	var contentID, permID float64
	for _, a := range res[0].(map[string]any)["attrs"].([]any) {
		m := a.(map[string]any)
		switch m["attr_path"] {
		case "content":
			contentID = m["id"].(float64)
		case "file_permission":
			permID = m["id"].(float64)
		}
	}
	envs := into(t, call(t, e.h, "GET", "/envs", nil))["envs"].([]any)
	for _, v := range envs {
		m := v.(map[string]any)
		if m["name"] == "dev" && (m["status"] != "drift" || m["targets"].([]any)[0].(map[string]any)["drift"].(float64) != 1) {
			t.Fatalf("overview should show drift: %v", m)
		}
	}

	// copy: preview, then apply with the previewed sha
	prev := into(t, call(t, e.h, "POST", "/drift/"+itoa(int(contentID))+"/action", map[string]any{"action": "copy"}))
	if !strings.Contains(prev["diff"].(string), `+  content         = "changed outside"`) {
		t.Fatalf("%v", prev["diff"])
	}
	if b, _ := os.ReadFile(filepath.Join(e.root, "terraform/envs/dev/files.tf")); strings.Contains(string(b), "changed outside") {
		t.Fatal("preview must not write")
	}
	if rec := call(t, e.h, "POST", "/drift/"+itoa(int(contentID))+"/action", map[string]any{"action": "copy", "apply": true, "sha": "stale"}); rec.Code != 409 {
		t.Fatalf("stale sha: %d", rec.Code)
	}
	if rec := call(t, e.h, "POST", "/drift/"+itoa(int(contentID))+"/action", map[string]any{"action": "copy", "apply": true, "sha": prev["sha"]}); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	b, _ := os.ReadFile(filepath.Join(e.root, "terraform/envs/dev/files.tf"))
	if !strings.Contains(string(b), `"changed outside"`) {
		t.Fatalf("%s", b)
	}

	// ignore: adds lifecycle.ignore_changes
	prev = into(t, call(t, e.h, "POST", "/drift/"+itoa(int(permID))+"/action", map[string]any{"action": "ignore"}))
	if !strings.Contains(prev["diff"].(string), "+    ignore_changes = [file_permission]") {
		t.Fatalf("%v", prev["diff"])
	}

	// revert: an ordinary plan
	rec = call(t, e.h, "POST", "/drift/"+itoa(int(permID))+"/action", map[string]any{"action": "revert"})
	if rec.Code != 202 || into(t, rec)["run"].(map[string]any)["kind"] != "tf.plan" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}

func TestSystemViewOverHTTP(t *testing.T) {
	if _, err := exec.LookPath("ansible-inventory"); err != nil {
		t.Skip("ansible not installed")
	}
	withFakeTerraform(t)
	root := fixture(t, "system-lab")
	st, _ := store.Open(filepath.Join(t.TempDir(), "s.db"))
	defer st.Close()
	a := New(root, "test", events.NewBus(), st)
	h := chi.NewRouter()
	a.Routes(h)
	sys := func(q string) map[string]any {
		rec := call(t, h, "GET", "/graph/system?root=infra&env=dev"+q, nil)
		if rec.Code != 200 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		return into(t, rec)
	}

	// not initialised: the architecture is drawn, every host is unlinked with the reason
	m := sys("")
	if !strings.Contains(m["state_error"].(string), "isn't initialised") {
		t.Fatalf("%v", m["state_error"])
	}
	if _, ok := m["state_pulled_at"]; ok {
		t.Fatal("no state, no pull time")
	}
	s := m["system"].(map[string]any)
	if len(s["unlinked"].([]any)) != 3 || len(s["containers"].([]any)) != 1 {
		t.Fatalf("%v", s)
	}

	// initialised with state: hosts land on the instances they run on
	if err := os.MkdirAll(filepath.Join(root, "infra", ".terraform"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_STATE_FILE", abs(t, "../../testdata/terraform/state_system.json"))
	_ = st.SetFacts("ansible#dev", "app-1", `{"ansible_distribution":"Ubuntu","ansible_distribution_version":"24.04","groundwork_listen_at":"2026-10-01T10:00:00Z","tcp_listen":[{"port":22,"protocol":"tcp","address":"0.0.0.0","name":"sshd"}]}`)
	m = sys("&refresh=1")
	if m["state_error"] != "" {
		t.Fatalf("%v", m["state_error"])
	}
	if at, err := time.Parse(time.RFC3339, m["state_pulled_at"].(string)); err != nil || time.Since(at) > time.Minute {
		t.Fatalf("state_pulled_at %v %v", m["state_pulled_at"], err)
	}
	s = m["system"].(map[string]any)
	app := s["servers"].(map[string]any)["aws_instance.app"].([]any)
	if len(app) != 2 {
		t.Fatalf("%v", s["servers"])
	}
	h1 := app[0].(map[string]any)
	if h1["name"] != "app-1" || h1["instance"] != "aws_instance.app[0]" || h1["os"] != "Ubuntu 24.04" {
		t.Fatalf("%v", h1)
	}
	var roles []string
	for _, r := range h1["roles"].([]any) {
		roles = append(roles, r.(map[string]any)["name"].(string))
	}
	if strings.Join(roles, ",") != "base,app" {
		t.Fatalf("%v", roles)
	}
	// the groups that list the host, for the boxes the view draws
	var groups []string
	for _, g := range h1["groups"].([]any) {
		groups = append(groups, g.(map[string]any)["name"].(string))
	}
	if strings.Join(groups, ",") != "app,monitoring" {
		t.Fatalf("groups %v", groups)
	}
	flags := map[float64]string{}
	for _, p := range h1["ports"].([]any) {
		p := p.(map[string]any)
		f, _ := p["flag"].(string)
		flags[p["port"].(float64)] = f
	}
	// 22: declared (ssh), allowed, listening; 8080: the role's port, no rule allows it
	if flags[22] != "" || flags[8080] != "blocked" {
		t.Fatalf("%v", flags)
	}
	if u := s["unlinked"].([]any); len(u) != 1 || u[0].(map[string]any)["name"] != "stray" {
		t.Fatalf("%v", u)
	}
	if len(s["rules"].(map[string]any)["aws_security_group.app"].([]any)) != 1 {
		t.Fatalf("%v", s["rules"])
	}
	// a project without inventories: its one inventory pairs with every stack
	cfg := filepath.Join(root, "groundwork.yaml")
	y, _ := os.ReadFile(cfg)
	if err := os.WriteFile(cfg, []byte(strings.Replace(string(y), "      inventories:\n        dev: inventory/dev.yml\n", "", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	a.reloadConfig()
	m = sys("")
	if sc := m["scopes"].([]any); len(sc) != 1 || sc[0].(map[string]any)["env"] != "default" || len(m["system"].(map[string]any)["servers"].(map[string]any)) != 1 {
		t.Fatalf("%v", m["scopes"])
	}
	if call(t, h, "GET", "/graph/system?root=nope&env=dev", nil).Code != 400 {
		t.Fatal("bad root")
	}
}
