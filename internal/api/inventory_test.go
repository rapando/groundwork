package api

import (
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/rapando/groundwork/internal/events"
	"github.com/rapando/groundwork/internal/store"
)

func TestInventoryOverHTTPWithRealAnsible(t *testing.T) {
	if _, err := exec.LookPath("ansible-inventory"); err != nil {
		t.Skip("ansible not installed")
	}
	root := fixture(t, "ansible-lab")
	st, _ := store.Open(filepath.Join(t.TempDir(), "s.db"))
	defer st.Close()
	a := New(root, "test", events.NewBus(), st)
	h := chi.NewRouter()
	a.Routes(h)
	if rec := call(t, h, "POST", "/setup/apply", map[string]any{}); rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	e := &testEnv{a: a, h: h, root: root}

	inv := into(t, call(t, h, "GET", "/inventory", nil))
	if inv["error"] != nil {
		t.Fatalf("%v", inv["error"])
	}
	scope := inv["scope"].(map[string]any)
	if scope["project"] != "." || scope["env"] != "dev" {
		t.Fatalf("%v", scope)
	}
	if len(inv["hosts"].([]any)) != 4 {
		t.Fatalf("%v", inv["hosts"])
	}
	files := map[string]bool{}
	for _, f := range inv["files"].([]any) {
		files[f.(string)] = true
	}
	for _, want := range []string{"inventory/dev.yml", "inventory/group_vars/web.yml", "inventory/host_vars/web-1.yml", "group_vars/web.yml"} {
		if !files[want] {
			t.Errorf("files missing %s: %v", want, files)
		}
	}
	if pbs := inv["playbooks"].([]any); len(pbs) != 1 || pbs[0] != "site.yml" {
		t.Fatalf("%v", pbs)
	}

	// ping through the runs API, then the inventory shows reachability
	rec := call(t, h, "POST", "/runs", map[string]any{"kind": "ans.ping", "project": ".", "env": "dev"})
	if rec.Code != 202 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	waitRun(t, e, into(t, rec)["run"].(map[string]any)["id"].(float64), "succeeded")
	inv = into(t, call(t, h, "GET", "/inventory?project=.&env=dev", nil))
	reach := map[string]bool{}
	for _, x := range inv["hosts"].([]any) {
		m := x.(map[string]any)
		reach[m["name"].(string)] = m["reachable"].(map[string]any)["reachable"].(bool)
	}
	if !reach["web-1"] || reach["down-1"] {
		t.Fatalf("%v", reach)
	}
	for _, g := range inv["groups"].([]any) {
		m := g.(map[string]any)
		if m["name"] == "flaky" && m["down"].(float64) != 1 {
			t.Fatalf("group down count: %v", m)
		}
	}

	host := into(t, call(t, h, "GET", "/inventory/host?project=.&env=dev&host=web-2", nil))
	var nw map[string]any
	for _, r := range host["vars"].(map[string]any)["rows"].([]any) {
		if r.(map[string]any)["name"] == "nginx_workers" {
			nw = r.(map[string]any)
		}
	}
	if nw["value"] != "3" || nw["status"] != "ok" || nw["winner"].(map[string]any)["file"] != "group_vars/web.yml" {
		t.Fatalf("%v", nw)
	}
	if call(t, h, "GET", "/inventory/host?project=.&env=dev&host=nope", nil).Code != 404 {
		t.Fatal("unknown host")
	}
	if rec := call(t, h, "GET", "/inventory?project=.&env=nope", nil); into(t, rec)["error"] == nil {
		t.Fatal("unknown env should report an error")
	}
}
