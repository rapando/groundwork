package scaffold

import (
	"bytes"
	"embed"
	"fmt"
	"path"
	"sort"
	"strings"
	"text/template"

	"github.com/rapando/groundwork/v2/internal/config"
)

//go:embed presets
var presetFS embed.FS

// File is one rendered output file.
type File struct {
	Path       string `json:"path"`
	Content    string `json:"content"`
	Executable bool   `json:"executable,omitempty"`
	// Merge means: if the file exists, append only the lines it lacks
	// (used for .gitignore) instead of treating it as a conflict.
	Merge bool `json:"merge,omitempty"`
}

type tplData struct {
	*Form
	FirstEnv string
	Binary   string // terraform | tofu ("" without one)
	FromTF   bool   // the inventory comes from Terraform's outputs
}

type entry struct {
	src, dst string
	exec     bool
}

// manifest is everything setup writes besides groundwork.yaml: nothing for
// Terraform alone, ansible.cfg for Ansible, and the inventory script that
// joins them when there are both.
func manifest(f *Form) []entry {
	sh := "presets/shared/"
	var m []entry
	if f.Ansible() {
		m = append(m, entry{src: sh + "ansible.cfg.tmpl", dst: "ansible/ansible.cfg"})
		if f.IaC() != "" {
			m = append(m, entry{src: sh + "tf_outputs.py.tmpl", dst: "ansible/inventory/tf_outputs.py", exec: true})
		}
	}
	return m
}

// Render produces the file set for a new repository, entirely in memory
// (this is also what the preview shows). groundwork.yaml is generated from
// the same form so the two can never disagree.
func Render(f *Form) ([]File, error) {
	f.Normalize()
	if errs := f.Validate(); len(errs) > 0 {
		return nil, errs[0]
	}
	d := tplData{Form: f, FirstEnv: f.Envs[0], Binary: f.IaC(), FromTF: f.IaC() != "" && f.Ansible()}

	var out []File
	for _, e := range manifest(f) {
		raw, err := presetFS.ReadFile(e.src)
		if err != nil {
			return nil, fmt.Errorf("preset file %s: %w", e.src, err)
		}
		tpl, err := template.New(e.src).Option("missingkey=error").Parse(string(raw))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", e.src, err)
		}
		var buf bytes.Buffer
		if err := tpl.Execute(&buf, d); err != nil {
			return nil, fmt.Errorf("%s: %w", e.src, err)
		}
		out = append(out, File{Path: e.dst, Content: buf.String(), Executable: e.exec})
	}

	cfg := ConfigFor(f)
	y, err := cfg.Marshal()
	if err != nil {
		return nil, err
	}
	if _, err := config.Parse(y, config.FileName); err != nil {
		return nil, fmt.Errorf("generated config is invalid: %w", err)
	}
	out = append(out, File{Path: config.FileName, Content: string(y)})
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// ConfigFor builds the groundwork.yaml matching a setup form.
func ConfigFor(f *Form) *config.Config {
	c := &config.Config{
		Version: 1, Mode: "standalone",
		Checks: config.Checks{OnSave: true},
		Ignore: []string{"vendor/", "node_modules/", "**/.terraform/"},
	}
	switch {
	case f.IaC() == "":
	case f.Layout == "dirs":
		c.Terraform.Binary = f.IaC()
		for _, e := range f.Envs {
			c.Terraform.Roots = append(c.Terraform.Roots, config.TFRoot{Path: "terraform/envs/" + e, Env: e})
		}
		c.Terraform.Modules = []string{"terraform/modules/*"}
	default:
		c.Terraform.Binary = f.IaC()
		envs := map[string]config.TFEnv{}
		for _, e := range f.Envs {
			envs[e] = config.TFEnv{Workspace: e, VarFiles: []string{"env/" + e + ".tfvars"}}
		}
		c.Terraform.Roots = []config.TFRoot{{Path: "terraform", Envs: envs}}
		c.Terraform.Modules = []string{"terraform/modules/*"}
	}
	if f.Ansible() {
		c.Ansible.Projects = []config.AnsibleProject{{Path: "ansible", Config: "ansible.cfg", VaultPasswordFile: f.VaultPasswordFile}}
	}
	for _, name := range CheckNames {
		if f.HasCheck(name) {
			c.Checks.Enabled = append(c.Checks.Enabled, name)
		}
	}
	if f.Ansible() {
		c.Checks.Enabled = append(c.Checks.Enabled, "syntax-check") // runs once there are playbooks
	}
	c.Checks.Enabled = append(c.Checks.Enabled, "secrets") // built in
	return c
}

// LintReq asks for one lint config file placed in Dir ("" or "." = repo root).
type LintReq struct {
	Kind string `json:"kind"` // tflint | ansible-lint | yamllint
	Dir  string `json:"dir"`
}

// LintConfigs renders just the optional lint config files, for embedded mode
// where groundwork must not scaffold the repo itself.
func LintConfigs(reqs []LintReq) ([]File, error) {
	names := map[string][2]string{
		"tflint":       {"presets/shared/tflint.hcl.tmpl", ".tflint.hcl"},
		"ansible-lint": {"presets/shared/ansible-lint.tmpl", ".ansible-lint"},
		"yamllint":     {"presets/shared/yamllint.tmpl", ".yamllint"},
	}
	var out []File
	for _, rq := range reqs {
		n, ok := names[rq.Kind]
		if !ok {
			return nil, fmt.Errorf("no lint config for %q", rq.Kind)
		}
		dir := path.Clean(rq.Dir)
		if rq.Dir == "" {
			dir = "."
		}
		if dir == ".." || strings.HasPrefix(dir, "../") || path.IsAbs(dir) {
			return nil, fmt.Errorf("lint config dir %q must be inside the repository", rq.Dir)
		}
		raw, err := presetFS.ReadFile(n[0])
		if err != nil {
			return nil, err
		}
		t, err := template.New(n[0]).Parse(string(raw))
		if err != nil {
			return nil, err
		}
		var b bytes.Buffer
		if err := t.Execute(&b, nil); err != nil {
			return nil, err
		}
		out = append(out, File{Path: path.Join(dir, n[1]), Content: b.String()})
	}
	return out, nil
}

// GitignoreEntry is the single line embedded mode adds to the repo's .gitignore.
func GitignoreEntry() File {
	return File{Path: ".gitignore", Content: ".groundwork/\n", Merge: true}
}
