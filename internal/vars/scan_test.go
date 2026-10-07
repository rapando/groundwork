package vars

import (
	"strings"
	"testing"

	"github.com/zclconf/go-cty/cty"
)

func TestScanCatchesTestPatterns(t *testing.T) {
	src := strings.Join([]string{
		`access_key = "AKIAIOSFODNN7EXAMPLE"`,
		`datadog_api_key = "f3c9a2b7e1d84c6f9a0b2c3d4e5f6a7b"`,
		`  db_password: Sup3r-S3cret!x`,
		`-----BEGIN RSA PRIVATE KEY-----`,
		`gh_token = "ghp_abcdefghijklmnopqrstuvwxyz0123456789"`,
		`api_token: "{{ vault_api_token }}"`, // template: fine
		`db_password = var.db_password`,      // reference: fine
		`password: change-me`,                // placeholder: fine
		`# old_password = "OldPassw0rd!x"`,   // comment: fine
		`admin_password = "aaaaaaaa"`,        // low entropy: fine
		`secret_ref = data.aws_secretsmanager_secret.x.id`,
		`tls_key: !vault |`,
		`  $ANSIBLE_VAULT;1.1;AES256`,
		`  6161616161`,
		`region = "eu-west-1"`,
		`vault_password_file: .vault_pass_x`,
		`ssh_private_key: ~/.ssh/id_ed25519`,
		`token_secret_arn = "arn:aws:secretsmanager:x"`,
	}, "\n")
	fs := Scan("x.tfvars", []byte(src))
	rules := map[int]string{}
	for _, f := range fs {
		rules[f.Line] = f.Rule
		if strings.Contains(f.Excerpt, "AKIA") || strings.Contains(f.Excerpt, "Sup3r") || strings.Contains(f.Excerpt, "f3c9a2") {
			t.Errorf("excerpt leaks the secret: %q", f.Excerpt)
		}
	}
	want := map[int]string{1: "aws-access-key-id", 2: "secret-assignment", 3: "secret-assignment", 4: "private-key", 5: "github-token"}
	for line, rule := range want {
		if rules[line] != rule {
			t.Errorf("line %d: got %q want %q", line, rules[line], rule)
		}
	}
	if len(fs) != len(want) {
		t.Errorf("false positives: %+v", fs)
	}
}

func TestEncryptedFilesAreSkipped(t *testing.T) {
	if Scan("v.yml", []byte("$ANSIBLE_VAULT;1.1;AES256\n6162\n")) != nil {
		t.Fatal("vault file")
	}
	sops := "db_password: ENC[AES256_GCM,data:abc,iv:x,tag:y,type:str]\nsops:\n    mac: ENC[...]\n"
	if Scan("s.enc.yaml", []byte(sops)) != nil {
		t.Fatal("sops file")
	}
	if Scannable("x.enc.yaml") || Scannable(".terraform.lock.hcl") || !Scannable("a.tfvars") || !Scannable("group_vars/all.yml") {
		t.Fatal("scope")
	}
}

func TestParseLiteralAndSetValue(t *testing.T) {
	for in, ok := range map[string]bool{`"x"`: true, `3`: true, `["a", "b"]`: true, `{ k = "v" }`: true, `var.x`: false, `x`: false, `"unterminated`: false, `1/0`: false, `-1/0`: false, `{ k = [1/0] }`: false, `0/1`: true} {
		_, err := ParseLiteral(in)
		if (err == nil) != ok {
			t.Errorf("%s: err=%v", in, err)
		}
	}
	src := "# keep\nreplicas = 2\n"
	out, err := SetValue([]byte(src), "t.tfvars", "region", cty.StringVal("eu-west-1"))
	if err != nil || !strings.Contains(string(out), "# keep") || !strings.Contains(string(out), `region   = "eu-west-1"`) {
		t.Fatalf("%v\n%s", err, out)
	}
	out, _ = SetValue(out, "t.tfvars", "replicas", cty.NumberIntVal(5))
	if !strings.Contains(string(out), "replicas = 5") || strings.Count(string(out), "replicas") != 1 {
		t.Fatalf("%s", out)
	}
	if _, err := SetValue(nil, "new.tfvars", "a", cty.True); err != nil {
		t.Fatal("creating from empty")
	}
}
