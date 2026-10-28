package doctor

import (
	"errors"
	"reflect"
	"testing"
)

// The hint uses an installer this machine has; the rest become alternatives.
// Without any installer, a download page wins over a command that can't run.
func TestHintsUseInstallersOnThisMachine(t *testing.T) {
	have := map[string]bool{}
	old := lookPath
	lookPath = func(f string) (string, error) {
		if have[f] {
			return "/bin/" + f, nil
		}
		return "", errors.New("not found")
	}
	t.Cleanup(func() { lookPath = old })

	cases := []struct {
		installers []string
		tool, hint string
	}{
		{[]string{"brew", "go"}, "ansible-lint", "brew install ansible-lint"},
		{[]string{"brew", "go"}, "tflint", "go install github.com/terraform-linters/tflint@latest"},
		{[]string{"brew"}, "tflint", "brew install tflint"},
		{[]string{"uv"}, "yamllint", "uv tool install yamllint"},
		{[]string{"pipx", "uv"}, "checkov", "pipx install checkov"},
		{nil, "tflint", "download it from https://github.com/terraform-linters/tflint/releases"},
		{nil, "yamllint", "brew install yamllint"}, // no page to fall back to: the first one
		{[]string{"brew"}, "terraform", "brew install hashicorp/tap/terraform"},
	}
	for _, c := range cases {
		have = map[string]bool{}
		for _, i := range c.installers {
			have[i] = true
		}
		if got := Hint(c.tool); got != c.hint {
			t.Errorf("%v %s: hint %q, want %q", c.installers, c.tool, got, c.hint)
		}
	}

	have = map[string]bool{"uv": true}
	h, alts := hints(specFor("ansible-lint").installs)
	if h != "uv tool install ansible-lint" || !reflect.DeepEqual(alts, []string{"brew install ansible-lint", "pipx install ansible-lint"}) {
		t.Fatalf("%q %v", h, alts)
	}
	if Hint("nope") != "" {
		t.Fatal("unknown tool has a hint")
	}
}
