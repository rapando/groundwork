package runner

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rapando/groundwork/internal/config"
	"github.com/rapando/groundwork/internal/events"
	"github.com/rapando/groundwork/internal/store"
)

type ansRig struct {
	r      *Runner
	st     *store.Store
	root   string
	labDir string
	cfg    *config.Config
}

// newAnsRig copies testdata/repos/ansible-lab into <root>/ops and runs real
// ansible against local-connection hosts (plus one unreachable ssh host).
func newAnsRig(t *testing.T) *ansRig {
	t.Helper()
	if _, err := exec.LookPath("ansible-playbook"); err != nil {
		t.Skip("ansible not installed")
	}
	root := t.TempDir()
	src, _ := filepath.Abs("../../testdata/repos/ansible-lab")
	filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		rel, _ := filepath.Rel(src, p)
		dst := filepath.Join(root, "ops", rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o755)
		}
		b, _ := os.ReadFile(p)
		return os.WriteFile(dst, b, 0o644)
	})
	lab := t.TempDir()
	os.WriteFile(filepath.Join(root, "ops/group_vars/all.yml"), []byte("---\nlab_dir: "+lab+"\n"), 0o644)
	os.WriteFile(filepath.Join(root, "ops/slow.yml"), []byte("---\n- name: Slow\n  hosts: web-1\n  gather_facts: false\n  tasks:\n    - name: Sleep\n      ansible.builtin.command: sleep 30\n"), 0o644)
	cfg := &config.Config{Version: 1, Mode: "standalone", Ansible: config.Ansible{Projects: []config.AnsibleProject{{
		Path: "ops", Config: "ansible.cfg", Inventories: map[string]string{"dev": "inventory/dev.yml", "prod": "inventory/dev.yml"},
	}}}}
	st, err := store.Open(filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	r := New(root, st, events.NewBus(), func() *config.Config { return cfg }, nil)
	r.KillGrace = 2 * time.Second
	t.Cleanup(func() { r.Shutdown(context.Background()) })
	return &ansRig{r, st, root, lab, cfg}
}

func (g *ansRig) wait(t *testing.T, id int64, want string) *Detail {
	t.Helper()
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) {
		d, _ := g.r.Detail(id)
		if d.Run.Status == want {
			return d
		}
		if d.Run.Status == store.StatusFailed && want != store.StatusFailed || d.Run.Status == store.StatusCancelled && want != store.StatusCancelled {
			p, _ := g.r.ReadLog(id, 0, 500, "", "")
			var lines []string
			for _, l := range p.Lines {
				lines = append(lines, l.Stage+": "+l.Text)
			}
			t.Fatalf("run ended %s (%s) while waiting for %s:\n%s", d.Run.Status, d.Summary.Error, want, strings.Join(lines, "\n"))
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", want)
	return nil
}

func (g *ansRig) submit(t *testing.T, req SubmitRequest) int64 {
	t.Helper()
	if req.Project == "" {
		req.Project = "ops"
	}
	run, err := g.r.Submit(req)
	if err != nil {
		t.Fatal(err)
	}
	return run.ID
}

func TestAnsibleDryRunChangesNothing(t *testing.T) {
	g := newAnsRig(t)
	id := g.submit(t, SubmitRequest{Kind: KindAnsCheck, Env: "dev", Playbook: "site.yml", Limit: "web-1"})
	d := g.wait(t, id, store.StatusSucceeded)
	if st := d.Summary.Ansible.Check["web-1"]; st.Changed != 1 {
		t.Fatalf("%+v", d.Summary.Ansible)
	}
	if entries, _ := os.ReadDir(g.labDir); len(entries) != 0 {
		t.Fatal("a dry run must not change anything")
	}
	res, _ := g.r.HostResults(id)
	var write *hostResult
	for i := range res {
		if res[i].Task == "web : Write config" {
			write = &res[i]
		}
	}
	if write == nil || write.Phase != "check" || write.Status != "changed" || !strings.Contains(write.Diff, "workers=4") {
		t.Fatalf("the dry run should show what would change: %+v", write)
	}
}

func TestAnsiblePlaybookIsApprovedThenRun(t *testing.T) {
	g := newAnsRig(t)
	id := g.submit(t, SubmitRequest{Kind: KindAnsPlaybook, Env: "dev", Playbook: "site.yml", Limit: "web-1"})
	d := g.wait(t, id, store.StatusWaitingApproval)
	if d.Target.ApprovalRequired || d.Summary.Ansible.Check["web-1"].Changed != 1 {
		t.Fatalf("%+v %+v", d.Target, d.Summary.Ansible)
	}
	if entries, _ := os.ReadDir(g.labDir); len(entries) != 0 {
		t.Fatal("nothing runs for real before approval")
	}
	if err := g.r.Approve(id, ApproveRequest{}); err != nil {
		t.Fatal(err)
	}
	d = g.wait(t, id, store.StatusSucceeded)
	b, err := os.ReadFile(filepath.Join(g.labDir, "web-1.conf"))
	if err != nil || string(b) != "workers=4 port=8080\n" {
		t.Fatalf("the approved run should have applied the config: %q %v", b, err)
	}
	if d.Summary.Ansible.Run["web-1"].Changed != 1 || d.Approval == nil {
		t.Fatalf("%+v %+v", d.Summary.Ansible, d.Approval)
	}
	st := stageStatus(d)
	if st["syntax"] != "succeeded" || st["check"] != "succeeded" || st["approve"] != "succeeded" || st["run"] != "succeeded" {
		t.Fatalf("%v", st)
	}
}

func TestAnsibleProdNeedsTypingAndFreshness(t *testing.T) {
	g := newAnsRig(t)
	id := g.submit(t, SubmitRequest{Kind: KindAnsPlaybook, Env: "prod", Playbook: "site.yml", Limit: "web-1"})
	g.wait(t, id, store.StatusWaitingApproval)
	if err := g.r.Approve(id, ApproveRequest{}); !errors.Is(err, ErrConfirmText) {
		t.Fatalf("%v", err)
	}
	// editing the project after the dry run invalidates it
	os.WriteFile(filepath.Join(g.root, "ops/roles/web/defaults/main.yml"), []byte("---\nnginx_workers: 9\n"), 0o644)
	if err := g.r.Approve(id, ApproveRequest{ConfirmText: "prod"}); !errors.Is(err, ErrStale) {
		t.Fatalf("%v", err)
	}
}

func TestAnsibleHostFailuresAreNamed(t *testing.T) {
	g := newAnsRig(t)
	id := g.submit(t, SubmitRequest{Kind: KindAnsPlaybook, Env: "dev", Playbook: "site.yml"})
	d := g.wait(t, id, store.StatusWaitingApproval) // dry-run failures can still be reviewed and approved
	if d.Summary.Ansible.Check["web-2"].Failures != 1 || d.Summary.Ansible.Check["down-1"].Unreachable != 1 {
		t.Fatalf("%+v", d.Summary.Ansible.Check)
	}
	g.r.Approve(id, ApproveRequest{})
	d = g.wait(t, id, store.StatusFailed)
	if !strings.Contains(d.Summary.Error, "1 host(s) failed: web-2") || !strings.Contains(d.Summary.Error, "1 unreachable: down-1") {
		t.Fatalf("%q", d.Summary.Error)
	}
	if _, err := os.Stat(filepath.Join(g.labDir, "web-1.conf")); err != nil {
		t.Fatal("healthy hosts still converge")
	}
}

func TestPingAndFactsUpdateInventoryState(t *testing.T) {
	g := newAnsRig(t)
	id := g.submit(t, SubmitRequest{Kind: KindAnsPing, Env: "dev"})
	g.wait(t, id, store.StatusSucceeded) // unreachable hosts are data, not a failed job
	st, _ := g.st.HostStatuses("ops#dev")
	if !st["web-1"].Reachable || st["down-1"].Reachable || !strings.Contains(st["down-1"].Msg, "Connection refused") {
		t.Fatalf("%+v", st)
	}
	id = g.submit(t, SubmitRequest{Kind: KindAnsFacts, Env: "dev", Limit: "web-1"})
	g.wait(t, id, store.StatusSucceeded)
	f, _ := g.st.GetFacts("ops#dev", "web-1")
	if f == nil {
		t.Fatal("no facts stored")
	}
	var facts map[string]any
	json.Unmarshal([]byte(f.JSON), &facts)
	if facts["ansible_os_family"] == nil || facts["ansible_env"] != nil {
		t.Fatalf("%v", facts)
	}
}

func TestAdhocMutatingNeedsConfirmation(t *testing.T) {
	g := newAnsRig(t)
	if _, err := g.r.Submit(SubmitRequest{Kind: KindAnsAdhoc, Project: "ops", Env: "dev", Pattern: "web-1", Module: "command", Args: "true"}); !errors.Is(err, ErrConfirmText) {
		t.Fatalf("%v", err)
	}
	id := g.submit(t, SubmitRequest{Kind: KindAnsAdhoc, Env: "dev", Pattern: "web-1", Module: "command", Args: "echo hi", ConfirmText: "dev"})
	d := g.wait(t, id, store.StatusSucceeded)
	if d.Approval == nil || d.Approval.ConfirmText != "dev" {
		t.Fatalf("a mutating ad-hoc command must leave an approval record: %+v", d.Approval)
	}
	ro := g.submit(t, SubmitRequest{Kind: KindAnsAdhoc, Env: "dev", Pattern: "web-1", Module: "ansible.builtin.setup", Args: "gather_subset=min"})
	if d := g.wait(t, ro, store.StatusSucceeded); d.Approval != nil {
		t.Fatal("read-only modules need no approval")
	}
}

func TestAnsibleTargetValidation(t *testing.T) {
	g := newAnsRig(t)
	for _, req := range []SubmitRequest{
		{Kind: KindAnsCheck, Project: "nope", Env: "dev", Playbook: "site.yml"},
		{Kind: KindAnsCheck, Project: "ops", Env: "staging", Playbook: "site.yml"},
		{Kind: KindAnsCheck, Project: "ops", Env: "dev", Playbook: "../../etc/passwd.yml"},
		{Kind: KindAnsCheck, Project: "ops", Env: "dev", Playbook: "missing.yml"},
		{Kind: KindAnsCheck, Project: "ops", Env: "dev", Playbook: "ansible.cfg"},
		{Kind: KindAnsCheck, Project: "ops", Env: "dev", Playbook: "site.yml", Limit: "--become"},
		{Kind: KindAnsCheck, Project: "ops", Env: "dev", Playbook: "site.yml", Tags: "a b"},
		{Kind: KindAnsAdhoc, Project: "ops", Env: "dev", Pattern: "-i", Module: "ping"},
		{Kind: KindAnsAdhoc, Project: "ops", Env: "dev", Pattern: "all", Module: "shell;ls"},
	} {
		if _, err := g.r.Submit(req); !errors.Is(err, ErrBadTarget) {
			t.Errorf("%+v accepted: %v", req, err)
		}
	}
}

func TestCancelAnsibleMidPlay(t *testing.T) {
	g := newAnsRig(t)
	id := g.submit(t, SubmitRequest{Kind: KindAnsCheck, Env: "dev", Playbook: "slow.yml"})
	// command tasks are skipped in check mode, so use an ad-hoc sleep instead
	g.r.Cancel(id)
	id = g.submit(t, SubmitRequest{Kind: KindAnsAdhoc, Env: "dev", Pattern: "web-1", Module: "command", Args: "sleep 30", ConfirmText: "dev"})
	for i := 0; i < 400; i++ {
		if d, _ := g.r.Detail(id); d.Run.Status == store.StatusRunning {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(1500 * time.Millisecond) // let ansible get into the task
	start := time.Now()
	g.r.Cancel(id)
	g.wait(t, id, store.StatusCancelled)
	if time.Since(start) > 10*time.Second {
		t.Fatalf("cancel took %s", time.Since(start))
	}
}
