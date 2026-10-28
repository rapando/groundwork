package runner

import (
	"testing"

	"github.com/rapando/groundwork/v2/internal/store"
)

// Approving the instant a plan reports waiting_approval must work, even if
// the plan job is still winding down; and the plan job's cleanup must not
// unregister the apply job that took over the run.
func TestApproveRightAfterWaitingApproval(t *testing.T) {
	g := newRig(t)
	run, err := g.r.Submit(SubmitRequest{Kind: KindPlan, Root: "terraform/envs/dev", Env: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	// simulate the gap: the plan job is still registered but has parked the run
	g.wait(t, run.ID, store.StatusWaitingApproval)
	plan := &job{id: run.ID, parked: true}
	g.r.mu.Lock()
	g.r.jobs[run.ID] = plan
	g.r.mu.Unlock()

	if err := g.r.Approve(run.ID, ApproveRequest{}); err != nil {
		t.Fatalf("approve while the plan job winds down: %v", err)
	}
	g.r.dropJob(plan) // the plan job's finish, arriving late
	g.r.mu.Lock()
	cur := g.r.jobs[run.ID]
	g.r.mu.Unlock()
	if cur == plan {
		t.Fatal("the parked plan job is still registered")
	}
	// the apply job (registered by Approve, untouched by the late drop) runs to the end
	g.wait(t, run.ID, store.StatusSucceeded)
}
