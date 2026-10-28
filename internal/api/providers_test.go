package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rapando/groundwork/v2/internal/terraform"
)

// fakeInit puts a terraform on PATH whose init writes FAKE_LOCK as the
// root's lock file and logs each call.
func fakeInit(t *testing.T, lock string) string {
	t.Helper()
	bin := t.TempDir()
	logf := filepath.Join(bin, "calls")
	script := `#!/bin/sh
echo "$@" >> "$FAKE_LOG"
for a; do case $a in -chdir=*) d=${a#-chdir=};; esac; done
printf '%s' "$FAKE_LOCK" > "$d/.terraform.lock.hcl"
`
	if err := os.WriteFile(filepath.Join(bin, "terraform"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_LOG", logf)
	t.Setenv("FAKE_LOCK", lock)
	return logf
}

func initCalls(logf string) int {
	b, _ := os.ReadFile(logf)
	return strings.Count(string(b), " init ")
}

func TestProvidersInstallOnceSourceAndVersionAreWritten(t *testing.T) {
	old := providerSettle
	providerSettle = 10 * time.Millisecond
	t.Cleanup(func() { providerSettle = old })
	logf := fakeInit(t, "provider \"registry.terraform.io/hashicorp/aws\" {\n  version     = \"5.31.0\"\n  constraints = \"~> 5.0\"\n}\n")
	e := newConfigured(t)
	events, stop := e.a.Bus.Subscribe()
	defer stop()
	backend := filepath.Join(e.root, "terraform/envs/dev/backend.tf")
	edit := func(providers string) {
		t.Helper()
		src := "terraform {\n  required_providers {\n" + providers + "  }\n}\n"
		if err := os.WriteFile(backend, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		e.a.OnFilesChanged([]string{"terraform/envs/dev/backend.tf"})
		time.Sleep(150 * time.Millisecond)
	}
	waitSync := func(status string) map[string]any {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case ev := <-events:
				if ev.Type != "providers.sync" {
					continue
				}
				var m map[string]any
				_ = json.Unmarshal(ev.Data, &m)
				if m["status"] == status {
					return m
				}
			case <-deadline:
				t.Fatalf("no providers.sync %s", status)
			}
		}
	}

	// the fixture's aws has a source but no version: nothing happens yet
	edit("    aws = { source = \"hashicorp/aws\" }\n")
	edit("    aws = { source = \"hashicorp/aws\", version = \"~> 5.0\" }\n")
	if m := waitSync("done"); m["root"] != "terraform/envs/dev" {
		t.Fatalf("%v", m)
	}
	if n := initCalls(logf); n != 1 {
		t.Fatalf("one init once complete, got %d", n)
	}
	// now the lock matches: saving again does nothing
	edit("    aws = { source = \"hashicorp/aws\", version = \"~>  5.0\" }\n")
	// half an edit doesn't parse: wait for the next save
	edit("    aws = { source = \"hashicorp/aws\", version = \n")
	if n := initCalls(logf); n != 1 {
		t.Fatalf("in sync or mid-edit: no init, got %d", n)
	}
	// a new provider with both written: installed
	edit("    aws = { source = \"hashicorp/aws\", version = \"~> 5.0\" }\n    random = { source = \"hashicorp/random\", version = \"~> 3.6\" }\n")
	waitSync("done")
	if n := initCalls(logf); n != 2 {
		t.Fatalf("got %d", n)
	}
	// the fake lock never gains random; the same requirements aren't retried
	edit("    aws = { source = \"hashicorp/aws\", version = \"~> 5.0\" }\n    random = { source = \"hashicorp/random\", version = \"~> 3.6\" }\n")
	if n := initCalls(logf); n != 2 {
		t.Fatalf("tried once per edit, got %d", n)
	}

	// turned off in groundwork.yaml: nothing runs
	cfgPath := filepath.Join(e.root, "groundwork.yaml")
	b, _ := os.ReadFile(cfgPath)
	if err := os.WriteFile(cfgPath, []byte(strings.Replace(string(b), "terraform:\n", "terraform:\n  install_providers: false\n", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	e.a.OnFilesChanged([]string{"groundwork.yaml"})
	if c := e.a.Config(); c == nil || c.Terraform.InstallProvidersOn() {
		t.Fatal("install_providers: false should be read")
	}
	edit("    tls = { source = \"hashicorp/tls\", version = \"~> 4.0\" }\n")
	if n := initCalls(logf); n != 2 {
		t.Fatalf("disabled, got %d", n)
	}
}

func TestProvidersInSync(t *testing.T) {
	aws := terraform.LockedProvider{Address: "registry.terraform.io/hashicorp/aws", Version: "5.31.0", Constraints: "~> 5.0"}
	req := []terraform.Requirement{{Name: "aws", Source: "hashicorp/aws", Version: "~>5.0"}}
	if !providersInSync(req, []terraform.LockedProvider{aws}) {
		t.Fatal("same constraint, spacing aside")
	}
	if providersInSync(nil, []terraform.LockedProvider{aws}) {
		t.Fatal("a provider no longer required is out of sync (to be removed)")
	}
	if providersInSync([]terraform.Requirement{{Source: "hashicorp/aws", Version: "~> 6.0"}}, []terraform.LockedProvider{aws}) {
		t.Fatal("a changed constraint is out of sync")
	}
}
