package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeInit puts a terraform on PATH whose init writes lock as the root's
// .terraform.lock.hcl (or fails, when lock is ""), and logs its arguments.
func fakeInit(t *testing.T, lock string) string {
	t.Helper()
	bin := t.TempDir()
	logf := filepath.Join(bin, "calls")
	script := `#!/bin/sh
echo "$@" >> "$FAKE_LOG"
for a; do case $a in -chdir=*) d=${a#-chdir=};; esac; done
if [ -z "$FAKE_LOCK" ]; then echo "╷" >&2; echo "│ Error: Failed to query available provider packages" >&2; exit 1; fi
printf '%s' "$FAKE_LOCK" > "$d/.terraform.lock.hcl"
`
	if err := os.WriteFile(filepath.Join(bin, "terraform"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_LOG", logf)
	t.Setenv("FAKE_LOCK", lock)
	t.Setenv("TF_DATA_DIR", "")
	return logf
}

func TestSyncProvidersInitsThenPrunesWhatTheLockDropped(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "terraform", "envs", "dev")
	installed := filepath.Join(dir, ".terraform", "providers", "registry.terraform.io", "hashicorp")
	for _, p := range []string{"aws/5.31.0/linux_amd64", "random/3.6.0/linux_amd64"} {
		if err := os.MkdirAll(filepath.Join(installed, p), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	logf := fakeInit(t, "provider \"registry.terraform.io/hashicorp/aws\" {\n  version     = \"5.31.0\"\n  constraints = \"~> 5.0\"\n}\n")
	r := New(root, nil, nil, nil, nil)

	res, err := r.SyncProviders(context.Background(), "terraform/envs/dev", "terraform")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(res.Removed, []string{"registry.terraform.io/hashicorp/random"}) {
		t.Fatalf("%+v", res)
	}
	if _, err := os.Stat(filepath.Join(installed, "aws", "5.31.0")); err != nil {
		t.Fatal("the provider still required stays")
	}
	calls, _ := os.ReadFile(logf)
	if got := strings.TrimSpace(string(calls)); got != "-chdir="+dir+" init -backend=false -input=false -no-color" {
		t.Fatalf("never touches the backend: %q", got)
	}
}

func TestSyncProvidersReportsInitErrorsAndNeverWaitsForARun(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "tf"), 0o755); err != nil {
		t.Fatal(err)
	}
	fakeInit(t, "")
	r := New(root, nil, nil, nil, nil)
	_, err := r.SyncProviders(context.Background(), "tf", "terraform")
	if err == nil || !strings.Contains(err.Error(), "Error: Failed to query available provider packages") {
		t.Fatalf("%v", err)
	}

	unlock, ok := r.tryLock("tf:tf") // a run holds the root
	if !ok {
		t.Fatal("lock")
	}
	defer unlock()
	if _, err := r.SyncProviders(context.Background(), "tf", "terraform"); !errors.Is(err, ErrRootBusy) {
		t.Fatalf("%v", err)
	}
	if _, err := r.SyncProviders(context.Background(), "tf", "no-such-terraform"); !errors.Is(err, ErrRootBusy) {
		t.Fatalf("busy is checked first: %v", err)
	}
}
