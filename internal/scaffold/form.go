// Package scaffold writes what a new repository starts from: groundwork.yaml,
// provider-neutral starter Terraform for each environment, ansible.cfg and,
// with both an IaC tool and Ansible, the inventory script that reads
// Terraform's hosts. Providers, resources and playbooks are the user's to
// write.
package scaffold

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/rapando/groundwork/v2/internal/config"
)

// Form is the setup form from the Set up screen.
type Form struct {
	Project string `json:"project"` // shown in the console
	// Tools is what the repository will use: terraform or tofu (not both),
	// and/or ansible. Only those tools' files and checks are set up.
	Tools  []string `json:"tools"`
	Envs   []string `json:"envs"`   // ordered
	Layout string   `json:"layout"` // dirs | workspaces: where the Terraform roots will live
	// VaultPasswordFile is where ansible-vault's password lives ("" = none:
	// it is typed in the console and kept in memory)
	VaultPasswordFile string   `json:"vault_password_file"`
	Checks            []string `json:"checks"` // config check names (fmt, validate, tflint, ...)
}

type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e FieldError) Error() string { return e.Field + ": " + e.Message }

var (
	envRe  = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	projRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
)

// CheckNames are the checks the form can turn on, in the order offered.
var CheckNames = []string{"fmt", "validate", "tflint", "checkov", "ansible-lint", "yamllint"}

// ToolNames are the tools a repository can be set up for.
var ToolNames = []string{"terraform", "tofu", "ansible"}

// tfChecks need Terraform or OpenTofu; the rest need Ansible.
var tfChecks = []string{"fmt", "validate", "tflint", "checkov"}

// DefaultTools is what a new repository is set up for when nobody chose:
// Terraform (OpenTofu when only tofu is installed) and Ansible.
func DefaultTools(lookPath func(string) (string, error)) []string {
	return []string{config.DefaultTFBinary(lookPath), "ansible"}
}

// IaC is the Terraform binary the form chose: terraform, tofu or "" for none.
func (f *Form) IaC() string {
	for _, t := range f.Tools {
		if t == "terraform" || t == "tofu" {
			return t
		}
	}
	return ""
}

// Ansible says whether the form chose Ansible.
func (f *Form) Ansible() bool { return in("ansible", f.Tools) }

// Normalize fills defaults and drops what the chosen tools don't use; call
// before Validate.
func (f *Form) Normalize() {
	if f.Tools == nil {
		f.Tools = []string{"terraform", "ansible"}
	}
	if !f.Ansible() {
		f.VaultPasswordFile = ""
	}
	var checks []string
	for _, c := range f.Checks {
		if in(c, tfChecks) && f.IaC() == "" || !in(c, tfChecks) && in(c, CheckNames) && !f.Ansible() {
			continue
		}
		checks = append(checks, c)
	}
	if f.Checks != nil {
		f.Checks = checks
	}
	if f.Layout == "" {
		f.Layout = "dirs"
	}
	if len(f.Envs) == 0 {
		f.Envs = []string{"dev", "staging", "prod"}
	}
}

// Validate returns every problem; the generated names end up in file paths
// and the inventory script, so they are restricted to safe character sets.
func (f *Form) Validate() []FieldError {
	var errs []FieldError
	add := func(field, msg string, a ...any) { errs = append(errs, FieldError{field, fmt.Sprintf(msg, a...)}) }

	if !projRe.MatchString(f.Project) {
		add("project", "use lowercase letters, digits and dashes")
	}
	for _, t := range f.Tools {
		if !in(t, ToolNames) {
			add("tools", "unknown tool %q", t)
		}
	}
	switch {
	case len(f.Tools) == 0:
		add("tools", "choose Terraform, OpenTofu or Ansible")
	case in("terraform", f.Tools) && in("tofu", f.Tools):
		add("tools", "choose Terraform or OpenTofu, not both")
	}
	if !in(f.Layout, []string{"dirs", "workspaces"}) {
		add("layout", "must be dirs or workspaces")
	}
	if len(f.Envs) == 0 {
		add("envs", "choose at least one environment")
	}
	seen := map[string]bool{}
	for _, e := range f.Envs {
		switch {
		case !envRe.MatchString(e):
			add("envs", "%q: use lowercase letters, digits and dashes", e)
		case seen[e]:
			add("envs", "duplicate environment %q", e)
		}
		seen[e] = true
	}
	if msg := config.CheckVaultPath(f.VaultPasswordFile); msg != "" {
		add("vault_password_file", "%s", msg)
	} else if strings.ContainsAny(f.VaultPasswordFile, "\r\n") || strings.TrimSpace(f.VaultPasswordFile) != f.VaultPasswordFile {
		add("vault_password_file", "enter a path such as ~/.vault-pass")
	}
	for _, c := range f.Checks {
		if !in(c, CheckNames) {
			add("checks", "unknown check %q", c)
		}
	}
	return errs
}

func (f *Form) HasCheck(name string) bool { return in(name, f.Checks) }

func in(v string, l []string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}
