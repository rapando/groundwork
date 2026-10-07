package runner

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rapando/groundwork/internal/store"
)

func TestDriftJobRecordsAndClears(t *testing.T) {
	g := newRig(t)
	sub, cancel := g.bus.Subscribe()
	defer cancel()
	run, err := g.r.Submit(SubmitRequest{Kind: KindDrift, Root: "terraform/envs/dev", Env: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	d := g.wait(t, run.ID, store.StatusSucceeded)
	if d.Summary.Drift == nil || d.Summary.Drift.Resources != 1 || d.Summary.Drift.Attrs < 2 {
		t.Fatalf("%+v", d.Summary.Drift)
	}
	rows, _ := g.st.ListDrift("terraform/envs/dev", "dev")
	by := map[string]store.DriftRow{}
	for _, r := range rows {
		by[r.AttrPath] = r
	}
	if r := by["content"]; r.Address != "local_file.motd" || r.Code != `"hello"` || r.Actual != `"changed outside"` || r.RunID != run.ID {
		t.Fatalf("%+v", rows)
	}
	if _, err := os.Stat(filepath.Join(g.r.runDir(run.ID), "drift.tfplan")); !os.IsNotExist(err) {
		t.Fatal("the refresh-only plan must never be kept around to apply")
	}
	for _, l := range g.tfLog(t) {
		if strings.HasPrefix(l, "start apply") {
			t.Fatal("drift detection must never apply")
		}
	}
	saw := false
	for len(sub) > 0 {
		if (<-sub).Type == "drift.updated" {
			saw = true
		}
	}
	if !saw {
		t.Fatal("drift.updated event")
	}

	// deleted-outside drift from the real capture
	t.Setenv("FAKE_DRIFT_SHOW_JSON", abs(t, "../../testdata/terraform/show_drift.json"))
	run, _ = g.r.Submit(SubmitRequest{Kind: KindDrift, Root: "terraform/envs/dev", Env: "dev"})
	g.wait(t, run.ID, store.StatusSucceeded)
	rows, _ = g.st.ListDrift("terraform/envs/dev", "dev")
	if len(rows) != 1 || rows[0].Action != "delete" || rows[0].Actual != "deleted outside Terraform" {
		t.Fatalf("%+v", rows)
	}

	// a clean check clears it
	t.Setenv("FAKE_DRIFT_EXIT", "0")
	run, _ = g.r.Submit(SubmitRequest{Kind: KindDrift, Root: "terraform/envs/dev", Env: "dev"})
	d = g.wait(t, run.ID, store.StatusSucceeded)
	if rows, _ = g.st.ListDrift("terraform/envs/dev", "dev"); len(rows) != 0 || d.Summary.Drift.Resources != 0 {
		t.Fatalf("%+v %+v", rows, d.Summary.Drift)
	}
}
