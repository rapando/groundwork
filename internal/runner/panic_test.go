package runner

import (
	"strings"
	"testing"
	"time"

	"github.com/rapando/groundwork/v2/internal/store"
)

// A panic on a job goroutine must not take the service down, must not leave
// the run "running" or the root locked, and must be reported.
func TestPanicInAJobFailsTheRunAndKeepsTheRunner(t *testing.T) {
	g := newRig(t)
	reported := make(chan string, 4) // the hook runs on the job goroutine
	g.r.OnPanic = func(name string, v any) { reported <- name + ": " + v.(string) }
	g.r.Hooks.BeforeStart = func(kind string) {
		if kind == KindPlan {
			panic("simulated bug")
		}
	}
	run, err := g.r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/dev", Env: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	d := g.wait(t, run.ID, store.StatusFailed)
	if !strings.Contains(d.Summary.Error, "internal error") || !strings.Contains(d.Summary.Error, "simulated bug") {
		t.Fatalf("summary error: %q", d.Summary.Error)
	}
	select {
	case r := <-reported:
		if !strings.Contains(r, "simulated bug") {
			t.Fatalf("OnPanic: %q", r)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnPanic was not called")
	}
	if g.r.Active() != 0 {
		t.Fatalf("job still registered: %d", g.r.Active())
	}
	// the runner, and the same root, still work
	g.r.Hooks.BeforeStart = nil
	run, err = g.r.Submit(SubmitRequest{Kind: KindInit, Root: "terraform/envs/dev", Env: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	g.wait(t, run.ID, store.StatusSucceeded)
}
