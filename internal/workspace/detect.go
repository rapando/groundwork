package workspace

import (
	"math"
	"path"
	"strings"
)

var iacExt = map[string]bool{
	".tf": true, ".tfvars": true, ".hcl": true, ".yml": true, ".yaml": true,
	".j2": true, ".ini": true, ".cfg": true,
}

// Detect scans root and returns what IaC it finds. It never runs any tool.
func Detect(root string, ignore []string) (*Report, error) {
	w, err := walk(root, ignore)
	if err != nil {
		return nil, err
	}
	r := &Report{
		Root:      root,
		FileCount: len(w.files),
		Empty:     len(w.files) == 0,
		Truncated: w.truncated,
	}
	r.TerraformRoots, r.TerraformModules = classifyTerraform(root, w)
	r.Ansible = buildAnsibleProjects(scanAnsible(root, w))
	r.CI = scanCI(root, w)
	r.Envs = buildEnvs(r.TerraformRoots, r.Ansible)

	iac := 0
	for _, f := range w.files {
		if iacExt[path.Ext(f)] || strings.HasPrefix(path.Base(f), "hosts") {
			iac++
		}
	}
	if len(w.files) > 0 {
		r.IaCRatio = math.Round(float64(iac)/float64(len(w.files))*100) / 100
	}
	r.Mode = "embedded"
	if r.IaCRatio > 0.6 {
		r.Mode = "standalone"
	}
	// ensure non-nil slices for stable JSON
	if r.TerraformRoots == nil {
		r.TerraformRoots = []TFDir{}
	}
	if r.TerraformModules == nil {
		r.TerraformModules = []TFDir{}
	}
	if r.Ansible == nil {
		r.Ansible = []AnsibleProject{}
	}
	if r.CI == nil {
		r.CI = []CIRef{}
	}
	if r.Envs == nil {
		r.Envs = []Env{}
	}
	return r, nil
}
