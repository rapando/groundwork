// Package workspace detects Terraform and Ansible code inside a repository.
package workspace

type Evidence struct {
	File string `json:"file"`
	Line int    `json:"line,omitempty"`
	Note string `json:"note"`
}

// TFDir is a directory containing Terraform code, classified as root or module.
type TFDir struct {
	Path         string     `json:"path"`
	Kind         string     `json:"kind"` // root | module
	Score        int        `json:"score"`
	Backend      string     `json:"backend,omitempty"`
	Providers    []string   `json:"providers,omitempty"`
	Tfvars       []string   `json:"tfvars,omitempty"`
	ReferencedBy []string   `json:"referenced_by,omitempty"`
	Evidence     []Evidence `json:"evidence"`
}

type AnsibleProject struct {
	Path        string     `json:"path"`
	Config      string     `json:"config,omitempty"`
	Playbooks   []string   `json:"playbooks,omitempty"`
	Roles       []string   `json:"roles,omitempty"`
	Inventories []string   `json:"inventories,omitempty"`
	VarsDirs    []string   `json:"vars_dirs,omitempty"`
	Evidence    []Evidence `json:"evidence"`
}

// CIRef is a CI step that runs an IaC tool.
type CIRef struct {
	File       string `json:"file"`
	Line       int    `json:"line,omitempty"`
	Tool       string `json:"tool"`
	WorkingDir string `json:"working_dir,omitempty"`
}

// Env joins Terraform and Ansible artefacts that share a normalised name.
type Env struct {
	Name      string   `json:"name"`
	Terraform []string `json:"terraform,omitempty"` // root paths, or "path#workspace"
	Ansible   []string `json:"ansible,omitempty"`   // inventory paths
}

type Report struct {
	Root             string           `json:"root,omitempty"`
	Mode             string           `json:"mode"` // standalone | embedded
	Empty            bool             `json:"empty"`
	FileCount        int              `json:"file_count"`
	Truncated        bool             `json:"truncated,omitempty"`
	IaCRatio         float64          `json:"iac_ratio"`
	TerraformRoots   []TFDir          `json:"terraform_roots"`
	TerraformModules []TFDir          `json:"terraform_modules"`
	Ansible          []AnsibleProject `json:"ansible"`
	CI               []CIRef          `json:"ci"`
	Envs             []Env            `json:"envs"`
}

// HasIaC reports whether anything was detected.
func (r *Report) HasIaC() bool {
	return len(r.TerraformRoots)+len(r.TerraformModules)+len(r.Ansible) > 0
}
