package checks

import (
	"testing"

	"github.com/rapando/groundwork/internal/config"
	"github.com/rapando/groundwork/internal/workspace"
)

func fixtureUnits(t *testing.T) []Unit {
	t.Helper()
	rep, err := workspace.Detect("../../testdata/repos/iac-only", nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.FromReport(rep)
	cfg.Checks.Enabled = []string{"fmt", "validate", "tflint", "ansible-lint", "yamllint", "syntax-check"}
	return BuildUnits(cfg, rep)
}

func TestBuildUnits(t *testing.T) {
	us := fixtureUnits(t)
	byID := map[string]Unit{}
	for _, u := range us {
		byID[u.ID] = u
	}
	dev := byID["tf:terraform/envs/dev"]
	if dev.Kind != KindRoot || len(dev.Tools) != 3 || len(dev.Deps) != 1 || dev.Deps[0] != "terraform/modules/network" {
		t.Fatalf("dev: %+v", dev)
	}
	mod := byID["tf:terraform/modules/network"]
	if mod.Kind != KindModule || len(mod.Tools) != 2 { // fmt, tflint: validate happens via roots
		t.Fatalf("module: %+v", mod)
	}
	ans := byID["ans:ansible"]
	if ans.Kind != KindAnsible || len(ans.Playbooks) != 1 || ans.Playbooks[0] != "ansible/playbooks/site.yml" || len(ans.Tools) != 3 {
		t.Fatalf("ansible: %+v", ans)
	}
}

func TestAffectedByModuleChange(t *testing.T) {
	us := fixtureUnits(t)
	ids := func(l []Unit) map[string]bool {
		m := map[string]bool{}
		for _, u := range l {
			m[u.ID] = true
		}
		return m
	}
	got := ids(Affected(us, "terraform/modules/network/main.tf"))
	for _, want := range []string{"tf:terraform/modules/network", "tf:terraform/envs/dev", "tf:terraform/envs/prod"} {
		if !got[want] {
			t.Errorf("editing the module must re-check %s (got %v)", want, got)
		}
	}
	if len(got) != 3 {
		t.Errorf("unexpected extra units: %v", got)
	}
	if got := ids(Affected(us, "terraform/envs/dev/main.tf")); len(got) != 1 || !got["tf:terraform/envs/dev"] {
		t.Errorf("editing dev should only re-check dev: %v", got)
	}
	if got := Affected(us, "README.md"); len(got) != 0 {
		t.Errorf("unmanaged file touched units: %v", got)
	}
}

func TestDisabledToolsDropUnit(t *testing.T) {
	rep, _ := workspace.Detect("../../testdata/repos/iac-only", nil)
	cfg := config.FromReport(rep)
	cfg.Checks.Enabled = []string{"yamllint"}
	for _, u := range BuildUnits(cfg, rep) {
		if u.Kind != KindAnsible {
			t.Fatalf("terraform unit with no enabled tools kept: %+v", u)
		}
	}
}
