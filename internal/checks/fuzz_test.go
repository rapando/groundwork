package checks

import (
	"os"
	"testing"
)

func FuzzToolParsers(f *testing.F) {
	for _, n := range []string{"validate_errors.json", "tflint.json", "checkov.json", "ansible_lint.json", "yamllint_parsable.txt", "fmt_list.txt", "syntax_yaml_error.txt"} {
		if b, err := os.ReadFile("../../testdata/checks/" + n); err == nil {
			f.Add(b)
		}
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		ParseValidate(b, "terraform/envs/dev")
		ParseTflint(b, "terraform/envs/dev")
		ParseCheckov(b, "terraform/envs/dev")
		ParseAnsibleLint(b, "ansible")
		ParseYamllint(b, "ansible")
		ParseFmtList(b, "terraform")
		ParseSyntaxCheck(b, "/repo", "ansible", "ansible/site.yml")
	})
}
