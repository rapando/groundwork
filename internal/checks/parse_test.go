package checks

import (
	"os"
	"testing"
)

func golden(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("../../testdata/checks/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestParseValidate(t *testing.T) {
	ds, err := ParseValidate(golden(t, "validate_errors.json"), "terraform/envs/dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 2 {
		t.Fatalf("got %d", len(ds))
	}
	// sorted top-to-bottom even though terraform emitted them in another order
	a, b := ds[0], ds[1]
	if a.Line != 7 || a.Message != "Unsupported argument" || a.File != "terraform/envs/dev/main.tf" ||
		a.Col != 3 || a.EndCol != 18 || a.Severity != SevError || a.Tool != "validate" {
		t.Fatalf("first: %+v", a)
	}
	if b.Line != 11 || b.Message != "Reference to undeclared input variable" {
		t.Fatalf("second: %+v", b)
	}
}

func TestFixFromTerraformHint(t *testing.T) {
	ds, _ := ParseValidate(golden(t, "validate_errors.json"), "m")
	fix := FixFromHint(ds[0])
	if fix == nil {
		t.Fatal("expected a fix from terraform's own suggestion")
	}
	e := fix.Edits[0]
	if fix.Title != "Replace with triggers_replace" || e.Old != "triggers_replac" || e.New != "triggers_replace" ||
		e.Line != 7 || e.Col != 3 || e.EndCol != 18 || e.File != "m/main.tf" {
		t.Fatalf("%+v", fix)
	}
	if FixFromHint(ds[1]) != nil {
		t.Fatal("no hint → no fix")
	}
	// a suggestion whose name length disagrees with the range must not produce an edit
	bad := ds[0]
	bad.EndCol = 99
	if FixFromHint(bad) != nil {
		t.Fatal("range mismatch should be rejected")
	}
}

func TestParseFmtList(t *testing.T) {
	ds := ParseFmtList(golden(t, "fmt_list.txt"), "terraform/envs/dev")
	if len(ds) != 1 || ds[0].File != "terraform/envs/dev/main.tf" || ds[0].Fix.Kind != "fmt" || ds[0].Fix.File != ds[0].File {
		t.Fatalf("%+v", ds)
	}
	if len(ParseFmtList(nil, "x")) != 0 {
		t.Fatal("empty output → no diagnostics")
	}
}

func TestParseTflint(t *testing.T) {
	ds, err := ParseTflint(golden(t, "tflint.json"), "terraform/modules/net")
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 3 {
		t.Fatalf("got %d", len(ds))
	}
	d := ds[0]
	if d.Code != "terraform_unused_declarations" || d.Severity != SevWarning || d.File != "terraform/modules/net/main.tf" ||
		d.Line != 1 || d.EndCol != 18 || d.Link == "" || d.Tool != "tflint" {
		t.Fatalf("%+v", d)
	}
	ds, _ = ParseTflint([]byte(`{"issues":[],"errors":[{"message":"Failed to load config","severity":"error"}]}`), "x")
	if len(ds) != 1 || ds[0].Severity != SevError || ds[0].File != "x" {
		t.Fatalf("%+v", ds)
	}
}

func TestParseCheckov(t *testing.T) {
	ds, err := ParseCheckov(golden(t, "checkov.json"), "terraform/envs/dev")
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) != 7 {
		t.Fatalf("got %d", len(ds))
	}
	d := ds[0]
	if d.File != "terraform/envs/dev/main.tf" || d.Line != 1 || d.EndLine != 3 || d.Code == "" || d.Detail != "aws_s3_bucket.b" {
		t.Fatalf("%+v", d)
	}
	// array form (several frameworks)
	ds, err = ParseCheckov([]byte(`[{"results":{"failed_checks":[{"check_id":"X","check_name":"n","file_path":"/a.tf","file_line_range":[2,4]}]}}]`), "b")
	if err != nil || len(ds) != 1 || ds[0].File != "b/a.tf" {
		t.Fatalf("%v %+v", err, ds)
	}
}

func TestParseAnsibleLintBothLocationShapes(t *testing.T) {
	ds, err := ParseAnsibleLint(golden(t, "ansible_lint.json"), "ansible")
	if err != nil {
		t.Fatal(err)
	}
	if len(ds) < 2 {
		t.Fatalf("got %d", len(ds))
	}
	var casing, nochanged *Diagnostic
	for i := range ds {
		switch ds[i].Code {
		case "name[casing]":
			if casing == nil {
				casing = &ds[i]
			}
		case "no-changed-when":
			if nochanged == nil {
				nochanged = &ds[i]
			}
		}
	}
	if casing == nil || nochanged == nil {
		t.Fatalf("missing expected rules: %+v", ds)
	}
	// positions.begin.{line,column}
	if casing.Line != 4 || casing.Col != 13 || casing.File != "ansible/site.yml" || casing.Severity != SevWarning {
		t.Fatalf("%+v", casing)
	}
	// lines.begin as a bare number: line is kept, column defaults to 1
	if nochanged.Line != 4 || nochanged.Col != 1 {
		t.Fatalf("%+v", nochanged)
	}
	if ds, err := ParseAnsibleLint(nil, "x"); err != nil || ds != nil {
		t.Fatal("empty output is not an error")
	}
}

func TestParseYamllint(t *testing.T) {
	ds := ParseYamllint(golden(t, "yamllint_parsable.txt"), "ansible")
	if len(ds) != 5 {
		t.Fatalf("got %d", len(ds))
	}
	if d := ds[1]; d.File != "ansible/warn.yml" || d.Line != 1 || d.Col != 7 || d.Severity != SevError || d.Code != "colons" {
		t.Fatalf("%+v", d)
	}
	if d := ds[3]; d.Code != "syntax" || d.File != "ansible/bad.yml" || d.Line != 3 {
		t.Fatalf("%+v", d)
	}
}

func TestParseSyntaxCheckModernAndLegacy(t *testing.T) {
	ds := ParseSyntaxCheck(golden(t, "syntax_yaml_error.txt"), "/repo", "ansible", "ansible/broken.yml")
	if len(ds) != 1 || ds[0].File != "ansible/broken.yml" || ds[0].Line != 6 || ds[0].Col != 12 ||
		ds[0].Message != "YAML parsing failed: While scanning a simple key could not find expected ':'." {
		t.Fatalf("%+v", ds)
	}
	ds = ParseSyntaxCheck(golden(t, "syntax_unknown_module.txt"), "/repo", "ansible", "ansible/unknown.yml")
	if len(ds) != 1 || ds[0].Line != 4 || ds[0].Col != 7 {
		t.Fatalf("%+v", ds)
	}
	legacy := "ERROR! 'foo' is not a valid attribute for a Play\n\nThe error appears to be in '/repo/ansible/site.yml': line 2, column 3, but may\nbe elsewhere in the file depending on the exact syntax problem.\n"
	ds = ParseSyntaxCheck([]byte(legacy), "/repo", "ansible", "ansible/site.yml")
	if len(ds) != 1 || ds[0].File != "ansible/site.yml" || ds[0].Line != 2 || ds[0].Col != 3 || ds[0].Message != "'foo' is not a valid attribute for a Play" {
		t.Fatalf("%+v", ds)
	}
	if ds := ParseSyntaxCheck([]byte("\nplaybook: x.yml\n"), "/repo", "ansible", "x.yml"); len(ds) != 0 {
		t.Fatalf("clean output produced %+v", ds)
	}
	ds = ParseSyntaxCheck([]byte("[ERROR]: something odd without origin\n"), "/repo", "ansible", "ansible/p.yml")
	if len(ds) != 1 || ds[0].File != "ansible/p.yml" || ds[0].Line != 1 {
		t.Fatalf("no-origin fallback: %+v", ds)
	}
}
