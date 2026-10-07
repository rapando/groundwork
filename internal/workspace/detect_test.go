package workspace

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite golden files")

func TestDetectGolden(t *testing.T) {
	repos, _ := filepath.Glob("../../testdata/repos/*")
	if len(repos) == 0 {
		t.Fatal("no fixtures")
	}
	for _, repo := range repos {
		name := filepath.Base(repo)
		t.Run(name, func(t *testing.T) {
			r, err := Detect(repo, nil)
			if err != nil {
				t.Fatal(err)
			}
			r.Root = ""
			got, _ := json.MarshalIndent(r, "", "  ")
			got = append(got, '\n')
			golden := filepath.Join("../../testdata/golden", name+".json")
			if *update {
				os.MkdirAll(filepath.Dir(golden), 0o755)
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("missing golden (run with -update): %v", err)
			}
			if string(want) != string(got) {
				t.Errorf("report differs from %s (run with -update to accept)\n%s", golden, got)
			}
		})
	}
}

func TestEmptyRepo(t *testing.T) {
	r, err := Detect(t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Empty || r.HasIaC() {
		t.Fatalf("%+v", r)
	}
}

func TestIgnoreRespected(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "vendor/x"), 0o755)
	os.WriteFile(filepath.Join(dir, "vendor/x/main.tf"), []byte(`provider "aws" {}`), 0o644)
	os.MkdirAll(filepath.Join(dir, "skipme"), 0o755)
	os.WriteFile(filepath.Join(dir, "skipme/main.tf"), []byte(`provider "aws" {}`), 0o644)
	os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("skipme/\n"), 0o644)
	r, _ := Detect(dir, nil)
	if r.HasIaC() {
		t.Fatalf("expected nothing, got %+v", r.TerraformRoots)
	}
}
