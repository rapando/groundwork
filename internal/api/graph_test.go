package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
