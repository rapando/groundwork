package api

import (
	"os"
	"strings"
	"testing"
	"time"
)

func withFakeTerraform(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", abs(t, "../../testdata/fake-terraform")+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_DATA", abs(t, "../../testdata/terraform"))
}

func abs(t *testing.T, p string) string {
	t.Helper()
	a, err := absPath(p)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func waitRun(t *testing.T, e *testEnv, id float64, want string) map[string]any {
	t.Helper()
	for i := 0; i < 600; i++ {
		rec := call(t, e.h, "GET", "/runs/"+itoa(int(id)), nil)
		m := into(t, rec)
		if m["run"].(map[string]any)["status"] == want {
			return m
		}
		time.Sleep(15 * time.Millisecond)
	}
	rec := call(t, e.h, "GET", "/runs/"+itoa(int(id)), nil)
	lg := call(t, e.h, "GET", "/runs/"+itoa(int(id))+"/log", nil)
	t.Fatalf("run %v never reached %s\n%s\n%s", id, want, rec.Body, lg.Body)
	return nil
}

func TestRunsEndToEndOverHTTP(t *testing.T) {
	withFakeTerraform(t)
	e := newConfigured(t)

	if rec := call(t, e.h, "POST", "/runs", map[string]any{"kind": "tf.plan", "root": "terraform/nope", "env": "dev"}); rec.Code != 400 {
		t.Fatalf("unknown root: %d", rec.Code)
	}
	if rec := call(t, e.h, "POST", "/runs", map[string]any{"kind": "tf.destroy", "root": "terraform/envs/dev", "env": "dev"}); rec.Code != 400 {
		t.Fatalf("unknown kind: %d", rec.Code)
	}
	if rec := call(t, e.h, "POST", "/runs", map[string]any{"kind": "tf.plan", "root": "../../etc", "env": "dev"}); rec.Code != 400 {
		t.Fatalf("path escape: %d", rec.Code)
	}

	rec := call(t, e.h, "POST", "/runs", map[string]any{"kind": "tf.plan", "root": "terraform/envs/prod", "env": "prod"})
	if rec.Code != 202 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	id := into(t, rec)["run"].(map[string]any)["id"].(float64)
	d := waitRun(t, e, id, "waiting_approval")
	if d["target"].(map[string]any)["approval_required"] != true {
		t.Fatalf("%v", d["target"])
	}

	plan := into(t, call(t, e.h, "GET", "/runs/"+itoa(int(id))+"/plan", nil))
	if plan["approvable"] != true || plan["confirm_text"] != "prod" || plan["plan"].(map[string]any)["create"].(float64) != 3 {
		t.Fatalf("%v", plan)
	}

	// log: paging, level filter, search
	lg := into(t, call(t, e.h, "GET", "/runs/"+itoa(int(id))+"/log?limit=2", nil))
	if len(lg["lines"].([]any)) != 2 || lg["next"].(float64) != 2 {
		t.Fatalf("%v", lg)
	}
	if got := into(t, call(t, e.h, "GET", "/runs/"+itoa(int(id))+"/log?q=PLAN%20TO%20CREATE", nil))["lines"].([]any); len(got) != 3 {
		t.Fatalf("search: %d", len(got))
	}
	if rec := call(t, e.h, "GET", "/runs/"+itoa(int(id))+"/log?level=loud", nil); rec.Code != 400 {
		t.Fatalf("bad level: %d", rec.Code)
	}

	if rec := call(t, e.h, "POST", "/runs/"+itoa(int(id))+"/approve", map[string]any{"confirm_text": "nope"}); rec.Code != 422 {
		t.Fatalf("wrong confirm: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, e.h, "POST", "/runs/"+itoa(int(id))+"/approve", map[string]any{"confirm_text": "prod"}); rec.Code != 202 {
		t.Fatalf("approve: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, e.h, "POST", "/runs/"+itoa(int(id))+"/approve", map[string]any{"confirm_text": "prod"}); rec.Code != 409 {
		t.Fatalf("double approve: %d", rec.Code)
	}
	d = waitRun(t, e, id, "succeeded")
	if d["approval"].(map[string]any)["confirm_text"] != "prod" {
		t.Fatalf("%v", d["approval"])
	}
	res := into(t, call(t, e.h, "GET", "/runs/"+itoa(int(id))+"/resources", nil))["resources"].([]any)
	if len(res) != 3 {
		t.Fatalf("%v", res)
	}
	if rec := call(t, e.h, "POST", "/runs/"+itoa(int(id))+"/cancel", nil); rec.Code != 409 {
		t.Fatalf("cancel finished run: %d", rec.Code)
	}

	// list, newest first, with counts and apply summary
	lst := into(t, call(t, e.h, "GET", "/runs", nil))["runs"].([]any)
	first := lst[0].(map[string]any)
	if first["id"].(float64) != id || first["apply"].(map[string]any)["added"].(float64) != 3 {
		t.Fatalf("%v", first)
	}

	for path, want := range map[string]int{"/runs/9999": 404, "/runs/abc": 400, "/runs/9999/log": 404, "/runs/9999/plan": 404} {
		if got := call(t, e.h, "GET", path, nil).Code; got != want {
			t.Errorf("%s: %d want %d", path, got, want)
		}
	}
}

func TestEnvsSummaryTracksPendingAndFailure(t *testing.T) {
	withFakeTerraform(t)
	e := newConfigured(t)
	envs := func() map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, v := range into(t, call(t, e.h, "GET", "/envs", nil))["envs"].([]any) {
			m := v.(map[string]any)
			out[m["name"].(string)] = m
		}
		return out
	}
	if m := envs(); m["dev"]["status"] != "unknown" || m["prod"] == nil {
		t.Fatalf("fresh: %v", m)
	}

	rec := call(t, e.h, "POST", "/runs", map[string]any{"kind": "tf.plan", "root": "terraform/envs/dev", "env": "dev"})
	id := into(t, rec)["run"].(map[string]any)["id"].(float64)
	waitRun(t, e, id, "waiting_approval")
	dev := envs()["dev"]
	tg := dev["targets"].([]any)[0].(map[string]any)
	if dev["status"] != "pending" || tg["pending"].(map[string]any)["create"].(float64) != 3 || tg["pending"].(map[string]any)["run_id"].(float64) != id {
		t.Fatalf("pending: %v", dev)
	}

	call(t, e.h, "POST", "/runs/"+itoa(int(id))+"/approve", map[string]any{})
	waitRun(t, e, id, "succeeded")
	if envs()["dev"]["status"] != "ok" || envs()["dev"]["targets"].([]any)[0].(map[string]any)["pending"] != nil {
		t.Fatalf("applied: %v", envs()["dev"])
	}

	t.Setenv("FAKE_FAIL_STAGE", "validate")
	rec = call(t, e.h, "POST", "/runs", map[string]any{"kind": "tf.plan", "root": "terraform/envs/prod", "env": "prod"})
	fid := into(t, rec)["run"].(map[string]any)["id"].(float64)
	waitRun(t, e, fid, "failed")
	if got := envs()["prod"]["status"]; got != "failed" {
		t.Fatalf("failed run should mark the env failed, got %v", got)
	}
	lr := envs()["prod"]["targets"].([]any)[0].(map[string]any)["last_run"].(map[string]any)
	if !strings.Contains(lr["error"].(string), "validate failed") || strings.Contains(lr["error"].(string), "AKIA") {
		t.Fatalf("%v", lr["error"])
	}
}

func TestSubmitRequiresSetup(t *testing.T) {
	h := newAPI(t, fixture(t, "iac-only"))
	if rec := call(t, h, "POST", "/runs", map[string]any{"kind": "tf.plan", "root": "a", "env": "b"}); rec.Code != 409 {
		t.Fatalf("%d", rec.Code)
	}
}

func TestLogDownloadIsPlainTextAndRedacted(t *testing.T) {
	withFakeTerraform(t)
	t.Setenv("FAKE_FAIL_STAGE", "init")
	e := newConfigured(t)
	rec := call(t, e.h, "POST", "/runs", map[string]any{"kind": "tf.init", "root": "terraform/envs/dev", "env": "dev"})
	id := into(t, rec)["run"].(map[string]any)["id"].(float64)
	waitRun(t, e, id, "failed")
	dl := call(t, e.h, "GET", "/runs/"+itoa(int(id))+"/log/download", nil)
	body := dl.Body.String()
	if dl.Code != 200 || !strings.HasPrefix(dl.Header().Get("Content-Type"), "text/plain") ||
		!strings.Contains(dl.Header().Get("Content-Disposition"), "run-1.log") {
		t.Fatalf("%d %v", dl.Code, dl.Header())
	}
	if strings.Contains(body, "AKIAIOSFODNN7EXAMPLE") || !strings.Contains(body, "init failed: boom") || !strings.Contains(body, "run failed") {
		t.Fatalf("%s", body)
	}
}

func TestPlanReviewOverHTTP(t *testing.T) {
	withFakeTerraform(t)
	t.Setenv("FAKE_SHOW_JSON", abs(t, "../../testdata/terraform/show_danger.json"))
	statePath := t.TempDir() + "/state.json"
	os.WriteFile(statePath, []byte(`{"serial":4,"lineage":"L"}`), 0o644)
	t.Setenv("FAKE_STATE_FILE", statePath)
	e := newConfigured(t)

	rec := call(t, e.h, "POST", "/runs", map[string]any{"kind": "tf.plan", "root": "terraform/envs/prod", "env": "prod"})
	id := itoa(int(into(t, rec)["run"].(map[string]any)["id"].(float64)))
	waitRun(t, e, into(t, rec)["run"].(map[string]any)["id"].(float64), "waiting_approval")

	p := into(t, call(t, e.h, "GET", "/runs/"+id+"/plan", nil))
	if len(p["danger"].([]any)) != 2 || p["approvable"] != true || len(p["stale"].([]any)) != 0 ||
		p["state"].(map[string]any)["serial"].(float64) != 4 || p["plan_ttl_seconds"].(float64) != 3600 {
		t.Fatalf("%v", p)
	}

	// lazy per-resource diff with source location lookup (none here: fake tf files)
	rd := call(t, e.h, "GET", "/runs/"+id+"/plan/resource?address=aws_db_instance.main", nil)
	diff := into(t, rd)["diff"].(map[string]any)
	if rd.Code != 200 || diff["action"] != "replace" || diff["changed"].([]any)[0].(map[string]any)["forces_replacement"] != true {
		t.Fatalf("%d %v", rd.Code, diff)
	}
	if call(t, e.h, "GET", "/runs/"+id+"/plan/resource?address=nope", nil).Code != 404 {
		t.Fatal("unknown address")
	}

	// typed confirmation, then acknowledgement, then freshness
	if rec := call(t, e.h, "POST", "/runs/"+id+"/approve", map[string]any{"confirm_text": "prod"}); rec.Code != 422 || into(t, rec)["error"].(map[string]any)["code"] != "ack_required" {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	os.WriteFile(statePath, []byte(`{"serial":5,"lineage":"L"}`), 0o644)
	rec = call(t, e.h, "POST", "/runs/"+id+"/approve", map[string]any{"confirm_text": "prod", "acknowledge_danger": true})
	if rec.Code != 409 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	er := into(t, rec)["error"].(map[string]any)
	if er["code"] != "stale_plan" || !strings.Contains(er["details"].(map[string]any)["reasons"].([]any)[0].(string), "serial 4 → 5") {
		t.Fatalf("%v", er)
	}

	// re-plan from the plan screen
	rp := call(t, e.h, "POST", "/runs/"+id+"/replan", nil)
	if rp.Code != 202 {
		t.Fatalf("%d %s", rp.Code, rp.Body)
	}
	nid := into(t, rp)["run"].(map[string]any)["id"].(float64)
	waitRun(t, e, nid, "waiting_approval")
	rec = call(t, e.h, "POST", "/runs/"+itoa(int(nid))+"/approve", map[string]any{"confirm_text": "prod", "acknowledge_danger": true})
	if rec.Code != 202 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	waitRun(t, e, nid, "succeeded")
	if into(t, call(t, e.h, "GET", "/runs/"+id, nil))["run"].(map[string]any)["status"] != "cancelled" {
		t.Fatal("re-plan discards the old plan")
	}
}
