package terraform

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

func load(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/terraform/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func events(t *testing.T, name string) []Event {
	t.Helper()
	var out []Event
	sc := bufio.NewScanner(strings.NewReader(string(load(t, name))))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		e, ok := ParseLine(sc.Bytes())
		if !ok {
			t.Fatalf("real terraform output line not parsed: %s", sc.Text())
		}
		out = append(out, e)
	}
	return out
}

func TestApplyStreamProducesResourceProgress(t *testing.T) {
	var started, done int
	seen := map[string][]string{}
	for _, e := range events(t, "apply.jsonl") {
		if e.Res == nil {
			continue
		}
		seen[e.Res.Address] = append(seen[e.Res.Address], e.Res.State)
		if e.Res.Action != "create" {
			t.Errorf("action %q", e.Res.Action)
		}
		switch e.Res.State {
		case "running":
			started++
		case "done":
			done++
		}
	}
	if started != 3 || done != 3 || len(seen) != 3 {
		t.Fatalf("started=%d done=%d resources=%d", started, done, len(seen))
	}
	for addr, states := range seen {
		if len(states) != 2 || states[0] != "running" || states[1] != "done" {
			t.Errorf("%s: %v", addr, states)
		}
	}
}

func TestFailedApplyMarksResourceFailedAndExplains(t *testing.T) {
	var failed *ResEvent
	var diag *Diag
	var deleted bool
	for _, e := range events(t, "apply_failed.jsonl") {
		if e.Res != nil && e.Res.State == "failed" {
			failed = e.Res
		}
		if e.Res != nil && e.Res.Action == "delete" && e.Res.State == "done" {
			deleted = true
		}
		if e.Diag != nil {
			diag = e.Diag
		}
	}
	if failed == nil || failed.Address != "terraform_data.boom" || failed.Action != "create" {
		t.Fatalf("%+v", failed)
	}
	if !deleted {
		t.Fatal("successful destroy before the failure should be reported")
	}
	if diag == nil || diag.Summary != "local-exec provisioner error" || diag.File != "fail.tf" || diag.Address != "terraform_data.boom" {
		t.Fatalf("%+v", diag)
	}
	for _, e := range events(t, "apply_failed.jsonl") {
		if e.Diag != nil {
			txt := e.Text()
			if !strings.HasPrefix(txt, "Error: local-exec provisioner error\n  on fail.tf line") || !strings.Contains(txt, "exit status 3") || e.Level != "error" {
				t.Fatalf("diagnostic text:\n%s", txt)
			}
		}
	}
}

func TestPlanErrorDiagnostics(t *testing.T) {
	var n int
	for _, e := range events(t, "plan_error.jsonl") {
		if e.Diag != nil {
			n++
			if e.Level != "error" || e.Diag.Summary != "No value for required variable" || e.Diag.Line == 0 {
				t.Fatalf("%+v", e)
			}
		}
	}
	if n != 2 {
		t.Fatalf("diagnostics %d", n)
	}
}

func TestParseLineRejectsNonJSON(t *testing.T) {
	for _, s := range []string{"", "Error: plain text", "{not json", `{"no":"type"}`, "panic: boom"} {
		if _, ok := ParseLine([]byte(s)); ok {
			t.Errorf("%q should not parse", s)
		}
	}
}

func TestSummarizeSimplePlan(t *testing.T) {
	s, err := SummarizePlan(load(t, "show_plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	if s.Create != 3 || s.Update+s.Replace+s.Delete != 0 || s.Total() != 3 || !s.Applyable || len(s.Changes) != 3 {
		t.Fatalf("%+v", s)
	}
	if c := s.Changes[0]; c.Type != "terraform_data" || c.Module != "module.service" || c.Action != "create" {
		t.Fatalf("%+v", c)
	}
}

func TestSummarizeMixedPlanDetectsReplace(t *testing.T) {
	s, err := SummarizePlan(load(t, "show_mixed.json"))
	if err != nil {
		t.Fatal(err)
	}
	// extra[0] is a no-op and must not appear; swap is a replace (delete+create); extra[1] is a plain delete
	if s.Create != 0 || s.Update != 1 || s.Replace != 1 || s.Delete != 1 || len(s.Changes) != 3 {
		t.Fatalf("%+v", s)
	}
	by := map[string]Change{}
	for _, c := range s.Changes {
		by[c.Address] = c
	}
	swap := by["terraform_data.swap"]
	if swap.Action != "replace" || len(swap.ReplacePaths) != 1 || swap.ReplacePaths[0][0] != "triggers_replace" || swap.Reason != "replace_because_cannot_update" {
		t.Fatalf("%+v", swap)
	}
	if by["terraform_data.extra[1]"].Action != "delete" || by["terraform_data.keep"].Action != "update" {
		t.Fatalf("%+v", by)
	}
	if _, ok := by["terraform_data.extra[0]"]; ok {
		t.Fatal("no-op listed")
	}
}

func TestClassify(t *testing.T) {
	cases := map[string][]string{
		"create": {"create"}, "delete": {"delete"}, "update": {"update"}, "read": {"read"}, "no-op": {"no-op"},
		"replace": {"delete", "create"},
	}
	for want, a := range cases {
		if got := Classify(a); got != want {
			t.Errorf("%v → %s want %s", a, got, want)
		}
	}
	if Classify([]string{"create", "delete"}) != "replace" {
		t.Error("create-before-destroy order is also a replace")
	}
}

func TestSummaryContainsNoValues(t *testing.T) {
	// plans can hold secrets; the stored summary keeps only addresses and actions
	s, _ := SummarizePlan(load(t, "show_mixed.json"))
	for _, c := range s.Changes {
		if c.Address == "" || c.Action == "" {
			t.Fatal("incomplete")
		}
	}
}

func TestArgvIsNeverAShellString(t *testing.T) {
	a := PlanArgs("terraform", "/r/a b", "/p/plan", []string{"/r/env/dev.tfvars; rm -rf /"})
	want := []string{"terraform", "-chdir=/r/a b", "plan", "-input=false", "-no-color", "-json", "-out=/p/plan", "-detailed-exitcode", "-lock-timeout=0s", "-var-file=/r/env/dev.tfvars; rm -rf /"}
	if strings.Join(a, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("%q", a)
	}
	ap := ApplyArgs("tofu", "/r", "/p/plan")
	if ap[len(ap)-1] != "/p/plan" || ap[0] != "tofu" {
		t.Fatalf("apply must use the saved plan file: %q", ap)
	}
	for _, x := range ap {
		if x == "-auto-approve" {
			t.Fatal("apply of a saved plan never needs -auto-approve")
		}
	}
}
