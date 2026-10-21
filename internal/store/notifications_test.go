package store

import (
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestNotificationsAddListRead(t *testing.T) {
	s := openTest(t)
	base := time.Now().Add(-time.Hour)
	add := func(n Notification) *Notification {
		t.Helper()
		ok, err := s.AddNotification(&n)
		if err != nil || !ok {
			t.Fatalf("add: %v %v", ok, err)
		}
		return &n
	}
	f := add(Notification{Event: EventFailed, RunID: 14, Kind: "tf.plan", Stage: "apply", Title: "Apply #14 failed on prod", Env: "prod",
		Cause: &Cause{IssueID: 3, RuleID: "tf-state-lock", Title: "State is locked", Action: "Unlock"}, DurationS: 161, At: base, Toast: true, Desktop: true})
	a := add(Notification{Event: EventApproval, RunID: 15, Title: "Plan #15 is ready for review", Plan: &PlanCounts{Create: 1, Delete: 2, Danger: 1}, At: base.Add(time.Minute)})
	add(Notification{Event: EventDrift, RunID: 16, Title: "Drift on prod: 2 resources", DriftCount: 2, At: base.Add(2 * time.Minute)})
	// one per (run, event)
	if ok, err := s.AddNotification(&Notification{Event: EventFailed, RunID: 14, Title: "again"}); ok || err != nil {
		t.Fatalf("duplicate accepted: %v %v", ok, err)
	}
	if !s.HasNotification(14, EventFailed) || s.HasNotification(14, EventSucceeded) {
		t.Fatal("HasNotification")
	}
	add(Notification{Event: EventSucceeded, RunID: 15, Title: "Apply #15 succeeded on prod", At: base.Add(3 * time.Minute)})

	l, err := s.ListNotifications(0)
	if err != nil || len(l) != 4 || l[0].RunID != 15 || l[0].Event != EventSucceeded || l[3].ID != f.ID {
		t.Fatalf("%v %+v", err, l)
	}
	got := l[3]
	if got.Cause == nil || got.Cause.Action != "Unlock" || !got.Toast || !got.Desktop || got.DurationS != 161 || got.Plan != nil || got.ReadAt != nil {
		t.Fatalf("%+v", got)
	}
	if g, _ := s.GetNotification(a.ID); g.Plan == nil || g.Plan.Danger != 1 || g.Plan.Delete != 2 {
		t.Fatalf("%+v", g)
	}
	if l, _ := s.ListNotifications(2); len(l) != 2 {
		t.Fatal("limit")
	}
	if u, fl := s.UnreadCounts(); u != 4 || fl != 1 {
		t.Fatalf("unread %d failed %d", u, fl)
	}
	if n, _ := s.MarkNotificationsRead(ReadFilter{IDs: []int64{f.ID}}); n != 1 {
		t.Fatal("by id")
	}
	if n, _ := s.MarkNotificationsRead(ReadFilter{RunID: 15}); n != 2 {
		t.Fatal("by run")
	}
	if u, fl := s.UnreadCounts(); u != 1 || fl != 0 {
		t.Fatalf("unread %d failed %d", u, fl)
	}
	if n, _ := s.MarkNotificationsRead(ReadFilter{}); n != 0 {
		t.Fatal("empty filter marks nothing")
	}
	if n, _ := s.MarkNotificationsRead(ReadFilter{All: true}); n != 1 || s.Unread() != 0 {
		t.Fatal("all")
	}
	if g, _ := s.GetNotification(f.ID); g.ReadAt == nil {
		t.Fatal("read_at")
	}
}

func TestNotificationsPrune(t *testing.T) {
	s := openTest(t)
	old := Notification{Event: EventFailed, RunID: 1, Title: "old", At: time.Now().Add(-31 * 24 * time.Hour)}
	cur := Notification{Event: EventFailed, RunID: 2, Title: "new"}
	s.AddNotification(&old)
	s.AddNotification(&cur)
	if n, err := s.PruneNotifications(time.Now().Add(-NotificationMaxAge)); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if l, _ := s.ListNotifications(10); len(l) != 1 || l[0].Title != "new" || l[0].At.IsZero() {
		t.Fatalf("%+v", l)
	}
}

func TestIssuesForRun(t *testing.T) {
	s := openTest(t)
	s.UpsertIssue("r1", "tf:a@dev", "", "run", time.Now(), map[string]any{"run_id": 7})
	s.UpsertIssue("r2", "tf:a@dev", "", "run", time.Now(), map[string]any{"run_id": 8})
	id, _, _ := s.UpsertIssue("r3", "tf:a@dev", "", "run", time.Now(), map[string]any{"run_id": 7})
	s.ResolveIssue(id, time.Now().Add(time.Second), "fixed")
	l, err := s.IssuesForRun(7)
	if err != nil || len(l) != 2 {
		t.Fatalf("%v %+v", err, l)
	}
	if l, _ := s.IssuesForRun(9); len(l) != 0 || l == nil {
		t.Fatal("none for 9")
	}
}

func TestPruneRunKV(t *testing.T) {
	s := openTest(t)
	r, _ := s.CreateRun("tf.plan", map[string]any{}, []string{}, "")
	s.SetKV("notify.run."+itoa(r.ID), "finish")
	s.SetKV("notify.run.999", "none")
	s.SetKV("other.999", "x")
	if n, err := s.PruneRunKV("notify.run."); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if _, ok, _ := s.GetKV("notify.run." + itoa(r.ID)); !ok {
		t.Fatal("kept run's mode removed")
	}
	if _, ok, _ := s.GetKV("other.999"); !ok {
		t.Fatal("unrelated key removed")
	}
}

func itoa(i int64) string { return strconv.FormatInt(i, 10) }
