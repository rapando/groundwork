// Package scaffold renders starter IaC layouts from embedded presets.
package scaffold

import (
	"fmt"
	"regexp"
	"strings"
)

// Form is the setup form from the Scaffold screen.
type Form struct {
	Preset    string   `json:"preset"`    // aws-envs | onprem
	Project   string   `json:"project"`   // used in names and README
	Backend   string   `json:"backend"`   // s3 | local | http
	Bucket    string   `json:"bucket"`    // s3 only
	Region    string   `json:"region"`    // aws only
	Envs      []string `json:"envs"`      // ordered
	Layout    string   `json:"layout"`    // dirs | workspaces
	Inventory string   `json:"inventory"` // terraform | static
	Roles     []string `json:"roles"`
	Vault     bool     `json:"vault"`
	Checks    []string `json:"checks"` // config check names (fmt, validate, tflint, ...)
	PreCommit bool     `json:"pre_commit"`
	CI        bool     `json:"ci"`
}

var Presets = []string{"aws-envs", "onprem"}

type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e FieldError) Error() string { return e.Field + ": " + e.Message }

var (
	envRe    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	roleRe   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	bucketRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	regionRe = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-\d$`)
	projRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
)

// Normalize fills defaults; call before Validate.
func (f *Form) Normalize() {
	if f.Preset == "" {
		f.Preset = "aws-envs"
	}
	if f.Layout == "" {
		f.Layout = "dirs"
	}
	if f.Inventory == "" {
		f.Inventory = "terraform"
	}
	if f.Backend == "" {
		if f.Preset == "aws-envs" {
			f.Backend = "s3"
		} else {
			f.Backend = "local"
		}
	}
	if len(f.Envs) == 0 {
		f.Envs = []string{"dev", "staging", "prod"}
	}
}

// Validate returns every problem; the generated names end up in file paths
// and HCL, so they are restricted to safe character sets.
func (f *Form) Validate() []FieldError {
	var errs []FieldError
	add := func(field, msg string, a ...any) { errs = append(errs, FieldError{field, fmt.Sprintf(msg, a...)}) }

	if !in(f.Preset, Presets) {
		add("preset", "unknown preset %q (available: %s)", f.Preset, strings.Join(Presets, ", "))
	}
	if !projRe.MatchString(f.Project) {
		add("project", "use lowercase letters, digits and dashes")
	}
	if !in(f.Layout, []string{"dirs", "workspaces"}) {
		add("layout", "must be dirs or workspaces")
	}
	if !in(f.Inventory, []string{"terraform", "static"}) {
		add("inventory", "must be terraform or static")
	}
	if !in(f.Backend, []string{"s3", "local", "http"}) {
		add("backend", "must be s3, local or http")
	}
	if f.Preset == "onprem" && f.Backend == "s3" {
		add("backend", "s3 needs the aws-envs preset")
	}
	if f.Preset == "aws-envs" {
		if !regionRe.MatchString(f.Region) {
			add("region", "enter an AWS region such as eu-west-1")
		}
		if f.Backend == "s3" && !bucketRe.MatchString(f.Bucket) {
			add("bucket", "enter a valid S3 bucket name")
		}
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
	for _, r := range f.Roles {
		if !roleRe.MatchString(r) {
			add("roles", "%q: use lowercase letters, digits and underscores", r)
		}
	}
	for _, c := range f.Checks {
		if !in(c, []string{"fmt", "validate", "tflint", "checkov", "ansible-lint", "yamllint"}) {
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
