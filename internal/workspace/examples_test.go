package workspace

import (
	"slices"
	"testing"
)

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
		{"../../examples/docker-env", "standalone", 1, 1, 1, []string{"dev"}},
		// the repository scripts/demo.sh drives end to end
		{"../../testdata/repos/app-with-infra", "embedded", 1, 0, 1, []string{"dev", "prod"}},
	}
	for _, c := range cases {
		t.Run(c.dir, func(t *testing.T) {
			r, err := Detect(c.dir, nil)
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
			if !slices.Equal(envs, c.envs) {
				t.Fatalf("envs %v, want %v", envs, c.envs)
			}
		})
	}
}

// docker-env: Terraform's ansible_hosts output is the inventory, read by the
// scaffold's script, and the Ansible project has a role per server.
func TestDockerEnvTerraformFeedsAnsible(t *testing.T) {
	r, _ := Detect("../../examples/docker-env", nil)
	if r.TerraformRoots[0].Path != "terraform/envs/dev" || r.Ansible[0].Path != "ansible" {
		t.Fatalf("%+v %+v", r.TerraformRoots, r.Ansible)
	}
	if inv := r.Ansible[0].Inventories; len(inv) != 1 || inv[0] != "ansible/inventory/tf_outputs.py" {
		t.Fatalf("inventories %v", inv)
	}
	roles := slices.Sorted(slices.Values(r.Ansible[0].Roles))
	if !slices.Equal(roles, []string{"ansible/roles/nginx", "ansible/roles/rabbitmq", "ansible/roles/redis"}) {
		t.Fatalf("roles %v", roles)
	}
}

func TestAppWithInfraTerraformFeedsAnsible(t *testing.T) {
	r, _ := Detect("../../testdata/repos/app-with-infra", nil)
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
