package store

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestRunLifecycleStagesAndApproval(t *testing.T) {
	s := open(t)
	r, err := s.CreateRun("tf.plan", map[string]string{"root": "a"}, []string{"terraform", "plan"}, "abc123")
	if err != nil || r.Status != StatusQueued || r.CreatedAt.IsZero() || r.Commit != "abc123" {
		t.Fatalf("%+v %v", r, err)
	}
	now := time.Now()
	stage := "plan"
	code := 2
	if err := s.UpdateRun(r.ID, RunUpdate{Status: StatusWaitingApproval, Stage: &stage, Started: &now, ExitCode: &code, Summary: map[string]int{"add": 3}}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetRun(r.ID)
	var sum map[string]int
	json.Unmarshal(got.Summary, &sum)
	if got.Status != StatusWaitingApproval || got.Stage != "plan" || *got.ExitCode != 2 || sum["add"] != 3 || got.StartedAt == nil {
		t.Fatalf("%+v", got)
	}
	// untouched fields survive partial updates
	s.UpdateRun(r.ID, RunUpdate{Status: StatusSucceeded})
	got, _ = s.GetRun(r.ID)
	if got.Stage != "plan" || got.Commit != "abc123" {
		t.Fatalf("partial update clobbered fields: %+v", got)
	}

	for _, n := range []string{"init", "validate", "plan"} {
		s.UpsertStage(RunStage{RunID: r.ID, Name: n, Status: "running", StartedAt: &now})
	}
	end := time.Now()
	s.UpsertStage(RunStage{RunID: r.ID, Name: "init", Status: "succeeded", EndedAt: &end}) // keeps started_at
	st, _ := s.ListStages(r.ID)
	if len(st) != 3 || st[0].Name != "init" || st[0].Status != "succeeded" || st[0].StartedAt == nil || st[2].Name != "plan" {
		t.Fatalf("%+v", st)
	}

	if a, _ := s.GetApproval(r.ID); a != nil {
		t.Fatal("no approval yet")
	}
	if err := s.SaveApproval(Approval{RunID: r.ID, PlanSHA256: "deadbeef", ApprovedBy: "sam", ApprovedAt: now, ConfirmText: "prod"}); err != nil {
		t.Fatal(err)
	}
	a, _ := s.GetApproval(r.ID)
	if a == nil || a.PlanSHA256 != "deadbeef" || a.ConfirmText != "prod" {
		t.Fatalf("%+v", a)
	}
	if _, err := s.GetRun(999); err != ErrNoRun {
		t.Fatalf("%v", err)
	}
}

func TestListRunsFiltersAndPaginates(t *testing.T) {
	s := open(t)
	for i := 0; i < 5; i++ {
		k := "tf.plan"
		if i%2 == 1 {
			k = "tf.init"
		}
		r, _ := s.CreateRun(k, nil, nil, "")
		if i == 4 {
			s.UpdateRun(r.ID, RunUpdate{Status: StatusFailed})
		}
	}
	all, _ := s.ListRuns(RunFilter{})
	if len(all) != 5 || all[0].ID != 5 {
		t.Fatalf("newest first expected: %v", all[0].ID)
	}
	if got, _ := s.ListRuns(RunFilter{Kind: "tf.init"}); len(got) != 2 {
		t.Fatalf("kind filter: %d", len(got))
	}
	if got, _ := s.ListRuns(RunFilter{Status: StatusFailed}); len(got) != 1 || got[0].ID != 5 {
		t.Fatalf("status filter")
	}
	page, _ := s.ListRuns(RunFilter{Limit: 2})
	next, _ := s.ListRuns(RunFilter{Limit: 2, Before: page[len(page)-1].ID})
	if len(page) != 2 || len(next) != 2 || next[0].ID != page[1].ID-1 {
		t.Fatalf("cursor paging broken")
	}
}

func TestInterruptedRunsAndPruning(t *testing.T) {
	s := open(t)
	running, _ := s.CreateRun("tf.plan", nil, nil, "")
	s.UpdateRun(running.ID, RunUpdate{Status: StatusRunning})
	s.UpsertStage(RunStage{RunID: running.ID, Name: "plan", Status: "running"})
	queued, _ := s.CreateRun("tf.plan", nil, nil, "")
	waiting, _ := s.CreateRun("tf.plan", nil, nil, "")
	s.UpdateRun(waiting.ID, RunUpdate{Status: StatusWaitingApproval})
	done, _ := s.CreateRun("tf.plan", nil, nil, "")
	s.UpdateRun(done.ID, RunUpdate{Status: StatusSucceeded})

	irs, err := s.InterruptedRuns()
	if err != nil || len(irs) != 2 {
		t.Fatalf("%v %v", irs, err)
	}
	for _, id := range []int64{running.ID, queued.ID} {
		r, _ := s.GetRun(id)
		if r.Status != StatusFailed || r.EndedAt == nil || !json.Valid(r.Summary) {
			t.Fatalf("%+v", r)
		}
	}
	if r, _ := s.GetRun(waiting.ID); r.Status != StatusWaitingApproval {
		t.Fatal("a run waiting for approval must survive a restart (its plan file is on disk)")
	}
	if st, _ := s.ListStages(running.ID); st[0].Status != "failed" {
		t.Fatalf("running stage should be failed: %+v", st)
	}

	// keep only the newest 1 finished run; active/waiting ones are never pruned
	ids, err := s.PruneRuns(1, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		if id == waiting.ID {
			t.Fatal("pruned a run that awaits approval")
		}
	}
	if _, err := s.GetRun(waiting.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetRun(done.ID); err != nil {
		t.Fatalf("newest run was pruned: %v", err)
	}
}

func TestRaiseIssueDedupes(t *testing.T) {
	s := open(t)
	s.RaiseIssue("tf-state-lock-possible", "terraform/envs/dev", map[string]int{"run": 1})
	s.RaiseIssue("tf-state-lock-possible", "terraform/envs/dev", map[string]int{"run": 2})
	s.RaiseIssue("tf-state-lock-possible", "terraform/envs/prod", nil)
	var n int
	s.DB.QueryRow(`SELECT COUNT(*) FROM issues`).Scan(&n)
	if n != 2 {
		t.Fatalf("got %d open issues", n)
	}
}

func TestHostStatusAndFacts(t *testing.T) {
	s := open(t)
	now := time.Now()
	s.SetHostStatus("ops#dev", HostStatus{Host: "web-1", Reachable: true, LatencyMS: 18, CheckedAt: now})
	s.SetHostStatus("ops#dev", HostStatus{Host: "web-1", Reachable: false, Msg: "timeout", CheckedAt: now})
	s.SetHostStatus("ops#prod", HostStatus{Host: "web-1", Reachable: true, CheckedAt: now})
	st, _ := s.HostStatuses("ops#dev")
	if len(st) != 1 || st["web-1"].Reachable || st["web-1"].Msg != "timeout" {
		t.Fatalf("%+v", st)
	}
	if f, _ := s.GetFacts("ops#dev", "web-1"); f != nil {
		t.Fatal("no facts yet")
	}
	s.SetFacts("ops#dev", "web-1", `{"a":1}`)
	s.SetFacts("ops#dev", "web-1", `{"a":2}`)
	f, _ := s.GetFacts("ops#dev", "web-1")
	all, _ := s.AllFacts("ops#dev")
	if f == nil || f.JSON != `{"a":2}` || len(all) != 1 {
		t.Fatalf("%+v %v", f, all)
	}
}

func TestDriftReplaceAndList(t *testing.T) {
	s := open(t)
	s.ReplaceDrift("r", "dev", 1, []DriftRow{{Address: "a.b", Action: "update", AttrPath: "tags.x", Code: `"t"`, Actual: `"c"`}, {Address: "a.c", Action: "delete"}})
	s.ReplaceDrift("r", "prod", 2, []DriftRow{{Address: "a.b", Action: "update", AttrPath: "x"}})
	dev, _ := s.ListDrift("r", "dev")
	if len(dev) != 2 || dev[0].Code != `"t"` || dev[0].RunID != 1 || dev[1].Action != "delete" {
		t.Fatalf("%+v", dev)
	}
	s.ReplaceDrift("r", "dev", 3, nil) // a clean re-check clears it
	if dev, _ = s.ListDrift("r", "dev"); len(dev) != 0 {
		t.Fatal("not cleared")
	}
	all, _ := s.ListDrift("", "")
	if len(all) != 1 {
		t.Fatal("other envs untouched")
	}
	if got, _ := s.GetDrift(all[0].ID); got == nil || got.Env != "prod" {
		t.Fatalf("%+v", got)
	}
	if _, err := s.GetDrift(999); err != ErrNoDrift {
		t.Fatal(err)
	}
}
