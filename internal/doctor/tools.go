// Package doctor inspects the user's environment.
package doctor

import (
	"context"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/rapando/groundwork/v2/internal/safego"
)

type Tool struct {
	Name    string `json:"name"`
	Found   bool   `json:"found"`
	Path    string `json:"path,omitempty"`
	Version string `json:"version,omitempty"`
	Hint    string `json:"hint,omitempty"` // install hint when missing, using an installer this machine has
	// Alternatives are the other ways to install it, when missing
	Alternatives []string `json:"alternatives,omitempty"`
}

// install is one way to install a tool: cmd, run with the installer via
// ("" = nothing to install first, such as a download page).
type install struct{ via, cmd string }

var (
	brew = func(f string) install { return install{"brew", "brew install " + f} }
	pipx = func(p string) install { return install{"pipx", "pipx install " + p} }
	uv   = func(p string) install { return install{"uv", "uv tool install " + p} }
	page = func(url string) install { return install{"", "download it from " + url} }
)

// specs lists each tool's installs, preferred first. tflint puts go first:
// not every Homebrew has a tflint formula.
type toolSpec struct {
	name     string
	args     []string
	re       string
	installs []install
}

var specs = []toolSpec{
	{"terraform", []string{"version"}, `Terraform v([\d.]+\S*)`, []install{brew("hashicorp/tap/terraform"), page("https://developer.hashicorp.com/terraform/install")}},
	{"tofu", []string{"version"}, `OpenTofu v([\d.]+\S*)`, []install{brew("opentofu"), page("https://opentofu.org/docs/intro/install/")}},
	{"ansible", []string{"--version"}, `ansible \[core ([\d.]+)`, []install{brew("ansible"), pipx("ansible-core"), uv("ansible-core")}},
	{"tflint", []string{"--version"}, `TFLint version ([\d.]+)`, []install{{"go", "go install github.com/terraform-linters/tflint@latest"}, brew("tflint"), page("https://github.com/terraform-linters/tflint/releases")}},
	{"ansible-lint", []string{"--version"}, `ansible-lint ([\d.]+)`, []install{brew("ansible-lint"), pipx("ansible-lint"), uv("ansible-lint")}},
	{"yamllint", []string{"--version"}, `yamllint ([\d.]+)`, []install{brew("yamllint"), pipx("yamllint"), uv("yamllint"), {"apt-get", "sudo apt-get install yamllint"}}},
	{"checkov", []string{"--version"}, `([\d.]+)`, []install{brew("checkov"), pipx("checkov"), uv("checkov")}},
	{"git", []string{"--version"}, `git version ([\d.]+)`, []install{brew("git"), {"apt-get", "sudo apt-get install git"}, page("https://git-scm.com/downloads")}},
}

func specFor(name string) *toolSpec {
	for i := range specs {
		if specs[i].name == name {
			return &specs[i]
		}
	}
	return nil
}

// lookPath finds installers; a variable so tests can pick which exist.
var lookPath = exec.LookPath

// hints picks the first install whose installer is on this machine (else
// the first one listed) and returns it with the rest, in their order.
func hints(installs []install) (string, []string) {
	pick := -1
	for i, in := range installs {
		if in.via == "" {
			continue // a download page is the last resort, wherever it's listed
		}
		if _, err := lookPath(in.via); err == nil {
			pick = i
			break
		}
	}
	if pick < 0 {
		pick = 0
		for i, in := range installs {
			if in.via == "" {
				pick = i
				break
			}
		}
	}
	var rest []string
	for i, in := range installs {
		if i != pick {
			rest = append(rest, in.cmd)
		}
	}
	return installs[pick].cmd, rest
}

// DetectTools looks up each tool on PATH and asks it for its version, in
// parallel, with a short timeout. Nothing here changes any state.
func DetectTools(ctx context.Context) []Tool {
	out := make([]Tool, len(specs))
	var wg sync.WaitGroup
	for i, s := range specs {
		wg.Add(1)
		safego.Go(nil, "detect "+s.name, nil, func() {
			defer wg.Done()
			t := Tool{Name: s.name}
			t.Hint, t.Alternatives = hints(s.installs)
			p, err := exec.LookPath(s.name)
			if err != nil {
				out[i] = t
				return
			}
			t.Found, t.Path, t.Hint, t.Alternatives = true, p, "", nil
			cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
			defer cancel()
			b, _ := exec.CommandContext(cctx, p, s.args...).CombinedOutput()
			if m := regexp.MustCompile(s.re).FindStringSubmatch(string(b)); m != nil {
				t.Version = strings.TrimSpace(m[1])
			}
			out[i] = t
		})
	}
	wg.Wait()
	return out
}

// Hint returns the install hint for a known tool, using an installer this
// machine has ("" if unknown).
func Hint(name string) string {
	for _, s := range specs {
		if s.name == name {
			h, _ := hints(s.installs)
			return h
		}
	}
	return ""
}
