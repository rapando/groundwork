// Package doctor inspects the user's environment.
package doctor

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Tool struct {
	Name    string `json:"name"`
	Found   bool   `json:"found"`
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
	Hint    string `json:"hint,omitempty"` // install hint when missing
}

var specs = []struct {
	name string
	args []string
	re   string
	hint string
}{
	{"terraform", []string{"version"}, `Terraform v([\d.]+\S*)`, "brew install terraform"},
	{"tofu", []string{"version"}, `OpenTofu v([\d.]+\S*)`, "brew install opentofu"},
	{"ansible", []string{"--version"}, `ansible \[core ([\d.]+)`, "pipx install ansible-core"},
	{"tflint", []string{"--version"}, `TFLint version ([\d.]+)`, "brew install tflint"},
	{"ansible-lint", []string{"--version"}, `ansible-lint ([\d.]+)`, "pipx install ansible-lint"},
	{"yamllint", []string{"--version"}, `yamllint ([\d.]+)`, "pipx install yamllint"},
	{"checkov", []string{"--version"}, `([\d.]+)`, "pipx install checkov"},
	{"git", []string{"--version"}, `git version ([\d.]+)`, "install git"},
}

// DetectTools looks up each tool on PATH and asks it for its version, in
// parallel, with a short timeout. Nothing here changes any state.
func DetectTools(ctx context.Context) []Tool {
	out := make([]Tool, len(specs))
	var wg sync.WaitGroup
	for i, s := range specs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			t := Tool{Name: s.name, Hint: s.hint}
			p, err := exec.LookPath(s.name)
			if err != nil {
				out[i] = t
				return
			}
			t.Found, t.Path, t.Hint = true, p, ""
			cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
			defer cancel()
			b, _ := exec.CommandContext(cctx, p, s.args...).CombinedOutput()
			if m := regexp.MustCompile(s.re).FindStringSubmatch(string(b)); m != nil {
				t.Version = strings.TrimSpace(m[1])
			}
			out[i] = t
		}()
	}
	wg.Wait()
	return out
}

// Hint returns the install hint for a known tool ("" if unknown).
func Hint(name string) string {
	for _, s := range specs {
		if s.name == name {
			return s.hint
		}
	}
	return ""
}
