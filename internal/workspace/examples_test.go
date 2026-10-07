package workspace

import "testing"

// The examples are documentation people copy; they must keep being detected as described.
func TestExamplesAreDetectedAsDocumented(t *testing.T) {
	cases := []struct {
		dir     string
		mode    string
		roots   int
		modules int
		ansible int
		envs    []string
	}{
		{"terraform-only", "standalone", 2, 1, 0, []string{"dev", "prod"}},
		{"ansible-only", "standalone", 0, 0, 1, []string{"dev", "prod"}},
		{"app-with-infra", "embedded", 1, 0, 1, []string{"dev", "prod"}},
	}
	for _, c := range cases {
		t.Run(c.dir, func(t *testing.T) {
			r, err := Detect("../../examples/"+c.dir, nil)
			if err != nil {
				t.Fatal(err)
			}
			if r.Mode != c.mode || len(r.TerraformRoots) != c.roots || len(r.TerraformModules) != c.modules || len(r.Ansible) != c.ansible {
				t.Fatalf("mode=%s roots=%d modules=%d ansible=%d (ratio %.2f)", r.Mode, len(r.TerraformRoots), len(r.TerraformModules), len(r.Ansible), r.IaCRatio)
			}
			var envs []string
			for _, e := range r.Envs {
				envs = append(envs, e.Name)
			}
			if len(envs) != len(c.envs) || envs[0] != c.envs[0] || envs[1] != c.envs[1] {
				t.Fatalf("envs %v", envs)
			}
		})
	}
}

func TestAppWithInfraTerraformFeedsAnsible(t *testing.T) {
	r, _ := Detect("../../examples/app-with-infra", nil)
	if r.TerraformRoots[0].Path != "deploy/terraform" || r.Ansible[0].Path != "ops/ansible" {
		t.Fatalf("%+v %+v", r.TerraformRoots, r.Ansible)
	}
	// the inventory script is detected as the (dynamic) inventory, and is not mistaken for an environment
	if inv := r.Ansible[0].Inventories; len(inv) != 1 || inv[0] != "ops/ansible/inventory/terraform.py" {
		t.Fatalf("inventories %v", inv)
	}
}

func TestInventoryScriptsAreNeverEnvironments(t *testing.T) {
	for _, p := range []string{"ansible/inventory/terraform.py", "ansible/inventory/tf_outputs.py", "inventory/aws_ec2.py", "inventory/hosts", "inventory/all.yml"} {
		if got := EnvForInventory(p); got != "" {
			t.Errorf("%s → env %q", p, got)
		}
	}
	if got := EnvForInventory("inventory/production.yml"); got != "prod" {
		t.Errorf("production.yml → %q", got)
	}
}
