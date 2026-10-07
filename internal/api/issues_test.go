package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIssuesOverHTTP(t *testing.T) {
	withFakeTerraform(t)
	lock := filepath.Join(t.TempDir(), "locked")
	t.Setenv("FAKE_LOCKED", lock)
	os.WriteFile(lock, nil, 0o644)
	e := newConfigured(t)

	rec := call(t, e.h, "POST", "/runs", map[string]any{"kind": "tf.plan", "root": "terraform/envs/dev", "env": "dev"})
	id := into(t, rec)["run"].(map[string]any)["id"].(float64)
	waitRun(t, e, id, "failed")
	e.a.Issues.ProcessRun(int64(id))

	l := into(t, call(t, e.h, "GET", "/issues", nil))
	open := l["open"].([]any)
	if len(open) != 1 {
		t.Fatalf("%v", l)
	}
	is := open[0].(map[string]any)
	if is["category"] != "state lock" || is["title"] != "State locked on terraform/envs/dev (dev)" {
		t.Fatalf("%v", is)
	}
	iid := itoa(int(is["id"].(float64)))
	d := into(t, call(t, e.h, "GET", "/issues/"+iid, nil))
	if len(d["checks"].([]any)) != 2 || !strings.Contains(d["excerpt"].(string), "Lock Info") {
		t.Fatalf("%v", d)
	}
	if rec := call(t, e.h, "POST", "/issues/"+iid+"/act", map[string]any{"step": 0, "confirm": "prod"}); rec.Code != 400 {
		t.Fatalf("wrong confirm: %d %s", rec.Code, rec.Body)
	}
	if rec := call(t, e.h, "POST", "/issues/"+iid+"/act", map[string]any{"step": 5}); rec.Code != 422 {
		t.Fatalf("bad step: %d", rec.Code)
	}
	rec = call(t, e.h, "POST", "/issues/"+iid+"/act", map[string]any{"step": 0, "confirm": "dev"})
	if rec.Code != 202 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	uid := into(t, rec)["run"].(map[string]any)["id"].(float64)
	waitRun(t, e, uid, "succeeded")
	e.a.Issues.ProcessRun(int64(uid))
	l = into(t, call(t, e.h, "GET", "/issues", nil))
	if len(l["open"].([]any)) != 0 || l["resolved_this_week"].(float64) != 1 {
		t.Fatalf("%v", l)
	}

	md := call(t, e.h, "GET", "/issues/export", nil)
	if !strings.Contains(md.Body.String(), "State locked on terraform/envs/dev (dev)") || md.Header().Get("Content-Type") != "text/markdown; charset=utf-8" {
		t.Fatalf("%s", md.Body)
	}
}

func TestDoctorAndOnboardingOverHTTP(t *testing.T) {
	withFakeTerraform(t)
	e := newConfigured(t)
	ob := into(t, call(t, e.h, "GET", "/onboarding", nil))
	done := map[string]bool{}
	for _, it := range ob["items"].([]any) {
		m := it.(map[string]any)
		done[m["id"].(string)] = m["done"].(bool)
	}
	if !done["scanned"] || done["plan"] || done["doctor"] || done["visualize"] || ob["tour_completed"] != false {
		t.Fatalf("%v", ob)
	}
	if rec := call(t, e.h, "POST", "/onboarding/event", map[string]any{"name": "rm -rf"}); rec.Code != 400 {
		t.Fatal("unknown events are refused")
	}
	call(t, e.h, "POST", "/onboarding/event", map[string]any{"name": "visualize.opened"})
	if rec := call(t, e.h, "GET", "/doctor", nil); into(t, rec)["report"] != nil {
		t.Fatal("no report yet")
	}
	rep := into(t, call(t, e.h, "POST", "/doctor/run", nil))["report"].(map[string]any)
	if len(rep["checks"].([]any)) == 0 {
		t.Fatal("empty doctor report")
	}
	ob = into(t, call(t, e.h, "POST", "/onboarding", map[string]any{"tour_completed": true}))
	done = map[string]bool{}
	for _, it := range ob["items"].([]any) {
		m := it.(map[string]any)
		done[m["id"].(string)] = m["done"].(bool)
	}
	if !done["doctor"] || !done["visualize"] || ob["tour_completed"] != true {
		t.Fatalf("%v", ob)
	}
	if s := into(t, call(t, e.h, "GET", "/drift/schedule", nil)); s["schedule"] != nil {
		t.Fatalf("%v", s)
	}
}
