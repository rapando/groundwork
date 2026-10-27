// Package scaffold renders starter IaC layouts from embedded presets.
package scaffold

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/rapando/groundwork/internal/config"
)

// Form is the setup form from the Scaffold screen.
type Form struct {
	Preset     string   `json:"preset"`      // aws-envs | gcp-envs | a key of Clouds | docker | onprem
	Project    string   `json:"project"`     // used in names and README
	Backend    string   `json:"backend"`     // s3 | gcs | local | http
	Bucket     string   `json:"bucket"`      // s3 and gcs only
	Region     string   `json:"region"`      // cloud presets only (not docker or onprem)
	GCPProject string   `json:"gcp_project"` // gcp only: the Google Cloud project ID
	S3Endpoint string   `json:"s3_endpoint"` // s3 backend on a token provider: its object storage endpoint
	Envs       []string `json:"envs"`        // ordered
	Layout     string   `json:"layout"`      // dirs | workspaces
	Inventory  string   `json:"inventory"`   // terraform | static
	Roles      []string `json:"roles"`
	Vault      bool     `json:"vault"`
	// VaultPasswordFile is where ansible-vault's password lives ("" = none:
	// it is typed in the console and kept in memory)
	VaultPasswordFile string   `json:"vault_password_file"`
	Checks            []string `json:"checks"` // config check names (fmt, validate, tflint, ...)
	PreCommit         bool     `json:"pre_commit"`
	CI                bool     `json:"ci"`
}

// Presets in the order they are offered; the first is the default.
var Presets = []string{"vultr", "aws-envs", "gcp-envs", "digitalocean", "hetzner", "linode", "docker", "onprem"}

type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

func (e FieldError) Error() string { return e.Field + ": " + e.Message }

var (
	envRe      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
	roleRe     = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	bucketRe   = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	regionRe   = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-\d$`)
	gcpRegRe   = regexp.MustCompile(`^[a-z]+-[a-z]+\d+$`)
	endpointRe = regexp.MustCompile(`^https://[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+(:\d{1,5})?/?$`)
	gcpProjRe  = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)
	projRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
)

// Normalize fills defaults; call before Validate.
func (f *Form) Normalize() {
	if f.Preset == "" {
		f.Preset = Presets[0]
	}
	if f.Layout == "" {
		f.Layout = "dirs"
	}
	if f.Inventory == "" {
		f.Inventory = "terraform"
	}
	if f.Backend == "" {
		switch f.Preset {
		case "aws-envs":
			f.Backend = "s3"
		case "gcp-envs":
			f.Backend = "gcs"
		default:
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
	if !in(f.Backend, []string{"s3", "gcs", "local", "http"}) {
		add("backend", "must be s3, gcs, local or http")
	}
	if _, s3ok := Clouds[f.Preset]; f.Backend == "s3" && f.Preset != "aws-envs" && !s3ok {
		add("backend", "s3 needs the aws-envs preset or a provider with S3-compatible object storage")
	}
	if f.Preset != "gcp-envs" && f.Backend == "gcs" {
		add("backend", "gcs needs the gcp-envs preset")
	}
	if f.Preset == "aws-envs" {
		if !regionRe.MatchString(f.Region) {
			add("region", "enter an AWS region such as eu-west-1")
		}
		if f.Backend == "s3" && !bucketRe.MatchString(f.Bucket) {
			add("bucket", "enter a valid S3 bucket name")
		}
	}
	if f.Preset == "gcp-envs" {
		if !gcpRegRe.MatchString(f.Region) {
			add("region", "enter a Google Cloud region such as europe-west1")
		}
		if !gcpProjRe.MatchString(f.GCPProject) {
			add("gcp_project", "enter a Google Cloud project ID such as acme-prod-123")
		}
		if f.Backend == "gcs" && !bucketRe.MatchString(f.Bucket) {
			add("bucket", "enter a valid GCS bucket name")
		}
	}
	if c, ok := Clouds[f.Preset]; ok {
		if !c.region.MatchString(f.Region) {
			add("region", "enter a %s %s such as %s", c.Label, c.RegionTerm, c.RegionHint)
		}
		if f.Backend == "s3" {
			if !bucketRe.MatchString(f.Bucket) {
				add("bucket", "enter a valid %s bucket name", c.Storage)
			}
			if !endpointRe.MatchString(f.S3Endpoint) {
				add("s3_endpoint", "enter the %s endpoint, such as %s", c.Storage, c.S3Hint)
			}
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
	if msg := config.CheckVaultPath(f.VaultPasswordFile); msg != "" {
		add("vault_password_file", "%s", msg)
	} else if strings.ContainsAny(f.VaultPasswordFile, "\r\n") || strings.TrimSpace(f.VaultPasswordFile) != f.VaultPasswordFile {
		add("vault_password_file", "enter a path such as ~/.vault-pass")
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
