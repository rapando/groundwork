package terraform

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiffsNeverContainSecrets(t *testing.T) {
	raw := load(t, "show_sensitive.json")
	if !strings.Contains(string(raw), "hunter2-SECRET") {
		t.Fatal("fixture should contain the raw secret (that's the point)")
	}
	diffs, err := ComputeDiffs(raw)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(diffs)
	for _, s := range []string{"hunter2", "n3w-SECRET"} {
		if strings.Contains(string(b), s) {
			t.Fatalf("secret %q leaked into diffs:\n%s", s, b)
		}
	}
	db := diffs["terraform_data.db"]
	byPath := map[string]AttrDiff{}
	for _, a := range append(db.Changed, db.Unchanged...) {
		byPath[a.Path] = a
	}
	// marked sensitive by terraform
	if a := byPath["input.password"]; a.Before != Sensitive || a.After != Sensitive {
		t.Fatalf("input.password: %+v", a)
	}
	// NOT marked by terraform (terraform_data copies input to output unmarked): still masked
	if a := byPath["output.password"]; a.Before != Sensitive {
		t.Fatalf("output.password: %+v", a)
	}
}

func TestDiffShapesFromRealPlan(t *testing.T) {
	diffs, err := ComputeDiffs(load(t, "show_sensitive.json"))
	if err != nil {
		t.Fatal(err)
	}
	db := diffs["terraform_data.db"]
	if db.Action != "replace" || len(db.ReplacePaths) != 1 || db.ReplacePaths[0] != "triggers_replace" {
		t.Fatalf("%+v", db)
	}
	if len(db.Changed) == 0 || !db.Changed[0].ForcesReplacement || db.Changed[0].Path != "triggers_replace[0]" {
		t.Fatalf("the attribute forcing replacement should come first: %+v", db.Changed)
	}
	if db.Changed[0].Before != "1" || db.Changed[0].After != "2" {
		t.Fatalf("%+v", db.Changed[0])
	}
	byPath := map[string]AttrDiff{}
	for _, a := range append(db.Changed, db.Unchanged...) {
		byPath[a.Path] = a
	}
	if a := byPath["id"]; a.Kind != "change" || a.After != Unknown {
		t.Fatalf("id should become unknown: %+v", a)
	}
	if a := byPath[`input.tags["team name"]`]; a.Kind != "same" || a.Before != `"core"` {
		t.Fatalf("keys that aren't identifiers must be quoted: %+v", byPath)
	}
	if a := byPath["input.ports[1]"]; a.Before != "6432" {
		t.Fatalf("list indexes: %+v", a)
	}
	if db.UnchangedCount == 0 {
		t.Fatal("unchanged attributes should be counted")
	}

	web := diffs["terraform_data.web"]
	wp := map[string]AttrDiff{}
	for _, a := range web.Changed {
		wp[a.Path] = a
	}
	if web.Action != "update" || wp["input.size"].Before != `"small"` || wp["input.size"].After != `"large"` {
		t.Fatalf("%+v", web.Changed)
	}
	if wp["input.deps"].After != Unknown || wp["input.deps"].ForcesReplacement {
		t.Fatalf("%+v", wp["input.deps"])
	}
}

func TestDiffDeleteAndNoop(t *testing.T) {
	diffs, _ := ComputeDiffs(load(t, "show_mixed.json"))
	if _, ok := diffs["terraform_data.extra[0]"]; ok {
		t.Fatal("no-op resources have no diff")
	}
	del := diffs["terraform_data.extra[1]"]
	if del.Action != "delete" || len(del.Changed) == 0 {
		t.Fatalf("%+v", del)
	}
	for _, a := range del.Changed {
		if a.Kind != "remove" || a.After != "" {
			t.Fatalf("a destroyed resource only has removals: %+v", a)
		}
	}
}

func TestDanger(t *testing.T) {
	s, _ := SummarizePlan(load(t, "show_danger.json"))
	ds := EvaluateDanger(s, nil, nil)
	if len(ds) != 2 {
		t.Fatalf("%+v", ds)
	}
	by := map[string]Danger{}
	for _, d := range ds {
		by[d.Address] = d
	}
	db := by["aws_db_instance.main"]
	if db.Category != "database" || db.Action != "replace" || !strings.Contains(db.Message, "destroyed and recreated") || !strings.Contains(db.Message, "snapshot") {
		t.Fatalf("%+v", db)
	}
	if by["aws_s3_bucket.logs[1]"].Category != "bucket" {
		t.Fatalf("%+v", by)
	}
	// updates are never dangerous, and safe types aren't flagged
	plain, _ := SummarizePlan(load(t, "show_mixed.json"))
	if got := EvaluateDanger(plain, nil, nil); len(got) != 0 {
		t.Fatalf("%+v", got)
	}
	// configured patterns and prevent_destroy elsewhere
	if got := EvaluateDanger(plain, []string{"terraform_data"}, nil); len(got) != 2 || got[0].Category != "custom" {
		t.Fatalf("%+v", got)
	}
	if got := EvaluateDanger(plain, nil, map[string]bool{"terraform_data.swap": true}); len(got) != 1 || got[0].Category != "protected" {
		t.Fatalf("%+v", got)
	}
	if typeAndName("module.a.module.b[0].aws_db_instance.main[2]") != "aws_db_instance.main" {
		t.Fatal(typeAndName("module.a.module.b[0].aws_db_instance.main[2]"))
	}
}

func TestDefaultPatternsMatchCommonTypes(t *testing.T) {
	for typ, want := range map[string]string{
		"aws_db_instance": "database", "aws_rds_cluster": "database", "google_sql_database_instance": "database",
		"aws_s3_bucket": "bucket", "google_storage_bucket": "bucket", "aws_ebs_volume": "volume",
		"google_compute_disk": "disk", "aws_instance": "", "aws_security_group": "",
	} {
		s := &PlanSummary{Changes: []Change{{Address: typ + ".x", Type: typ, Action: "delete"}}}
		got := ""
		if ds := EvaluateDanger(s, nil, nil); len(ds) > 0 {
			got = ds[0].Category
		}
		if got != want {
			t.Errorf("%s: %q want %q", typ, got, want)
		}
	}
}

func TestProtectedResourcesAndLocate(t *testing.T) {
	root := t.TempDir()
	write := func(p, s string) {
		os.MkdirAll(filepath.Dir(filepath.Join(root, p)), 0o755)
		os.WriteFile(filepath.Join(root, p), []byte(s), 0o644)
	}
	write("envs/prod/main.tf", "module \"db\" {\n  source = \"../../modules/db\"\n}\n")
	write("modules/db/main.tf", "# db\n\nresource \"aws_db_instance\" \"main\" {\n  lifecycle {\n    prevent_destroy = true\n  }\n}\n\ndata \"aws_ami\" \"x\" {}\n")
	write("envs/prod/.terraform/modules/modules.json", `{"Modules":[{"Key":"","Source":"","Dir":"."},{"Key":"db","Source":"../../modules/db","Dir":"../../modules/db"}]}`)
	write("envs/dev/x.tf", "resource \"aws_instance\" \"web\" {\n  lifecycle {\n    prevent_destroy = false\n  }\n}\n")

	p := ProtectedResources(root)
	if !p["aws_db_instance.main"] || p["aws_instance.web"] || len(p) != 1 {
		t.Fatalf("%v", p)
	}
	loc := Locate(root, filepath.Join(root, "envs/prod"), "module.db.aws_db_instance.main")
	if loc == nil || loc.File != "modules/db/main.tf" || loc.Line != 3 {
		t.Fatalf("%+v", loc)
	}
	if loc := Locate(root, filepath.Join(root, "envs/prod"), "module.db[0].data.aws_ami.x"); loc == nil || loc.Line != 9 {
		t.Fatalf("data source in indexed module: %+v", loc)
	}
	if Locate(root, filepath.Join(root, "envs/prod"), "module.nope.aws_x.y") != nil {
		t.Fatal("unknown module")
	}
	dirs := ModuleDirs(filepath.Join(root, "envs/prod"))
	if len(dirs) != 2 {
		t.Fatalf("%v", dirs)
	}
}

func TestComputeDriftFromRealAndDerivedPlans(t *testing.T) {
	dels, err := ComputeDrift(load(t, "show_drift.json")) // real: file changed → provider reports deleted
	if err != nil {
		t.Fatal(err)
	}
	d := dels["local_file.motd"]
	if d == nil || d.Action != "delete" || len(dels) != 1 {
		t.Fatalf("%+v", dels)
	}
	ups, _ := ComputeDrift(load(t, "show_drift_update.json"))
	u := ups["local_file.motd"]
	byPath := map[string]AttrDiff{}
	for _, a := range u.Changed {
		byPath[a.Path] = a
	}
	if u.Action != "update" || byPath["content"].Before != `"hello"` || byPath["content"].After != `"changed outside"` || byPath["file_permission"].After != `"0600"` {
		t.Fatalf("%+v", u.Changed)
	}
	// ComputeDiffs on a refresh-only plan has no resource changes
	if cs, _ := ComputeDiffs(load(t, "show_drift.json")); len(cs) != 0 {
		t.Fatalf("%v", cs)
	}
}
