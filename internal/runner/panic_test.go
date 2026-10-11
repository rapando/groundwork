package runner

import (
	"strings"
	"testing"

	"github.com/rapando/groundwork/internal/store"
)

// A panic on a job goroutine must not take the service down, must not leave
// the run "running" or the root locked, and must be reported.
func TestPanicInAJobFailsTheRunAndKeepsTheRunner(t *testing.T) {
	g := newRig(t)
	var reported []string
	g.r.OnPanic = func(name string, v any) { reported = append(reported, name+": "+v.(string)) }
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
	if len(reported) != 1 || !strings.Contains(reported[0], "simulated bug") {
		t.Fatalf("OnPanic: %v", reported)
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
