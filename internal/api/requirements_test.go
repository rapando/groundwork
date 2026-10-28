package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeGalaxy puts an ansible-galaxy on PATH that logs its arguments, working
// directory and ANSIBLE_HOME, one line per call.
func fakeGalaxy(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	logf := filepath.Join(bin, "calls")
	script := "#!/bin/sh\necho \"$* | $(pwd -P) | $ANSIBLE_HOME\" >> \"$FAKE_LOG\"\n"
	if err := os.WriteFile(filepath.Join(bin, "ansible-galaxy"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_LOG", logf)
	return logf
}

func galaxyCalls(t *testing.T, logf string) []string {
	t.Helper()
	b, _ := os.ReadFile(logf)
	s := strings.TrimSpace(string(b))
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func TestRequirementsInstallWhenTheFileChanges(t *testing.T) {
	old := providerSettle
	providerSettle = 10 * time.Millisecond
	t.Cleanup(func() { providerSettle = old })
	logf := fakeGalaxy(t)
	e := newConfigured(t)
	events, stop := e.a.Bus.Subscribe()
	defer stop()
	file := filepath.Join(e.root, "ansible", "requirements.yml")
	// edit saves src and waits for the install it starts, if any: an install
	// ends with done or failed; none starts within a second
	edit := func(src string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
		e.a.OnFilesChanged([]string{"ansible/requirements.yml"})
		quiet := time.After(time.Second)
		for {
			select {
			case ev := <-events:
				if ev.Type == "requirements.sync" && (strings.Contains(string(ev.Data), `"done"`) || strings.Contains(string(ev.Data), `"failed"`)) {
					return
				}
			case <-quiet:
				return
			}
		}
	}

	edit("collections:\n  - name: community.docker\n")
	calls := galaxyCalls(t, logf)
	if len(calls) != 1 {
		t.Fatalf("one install: %v", calls)
	}
	root, _ := filepath.EvalSymlinks(e.root)
	want := "install -r " + file + " | " + filepath.Join(root, "ansible") + " | " + filepath.Join(e.root, ".groundwork", "ansible-home")
	if calls[0] != want {
		t.Fatalf("in the project, into groundwork's ANSIBLE_HOME:\n got %s\nwant %s", calls[0], want)
	}
	edit("collections:\n  - name: community.docker\n") // unchanged
	edit("collections:\n  - name: community.docker\n  - name:\n")
	edit("collections:\n  - name: community.docker\n  - {name: community.general")
	if calls := galaxyCalls(t, logf); len(calls) != 1 {
		t.Fatalf("unchanged, unnamed or unparsable: no install, got %v", calls)
	}
	edit("collections:\n  - name: community.docker\n  - name: community.general\n    version: \">=9.0.0\"\nroles:\n  - geerlingguy.docker\n")
	if calls := galaxyCalls(t, logf); len(calls) != 2 {
		t.Fatalf("got %v", calls)
	}
	// a requirements file outside a configured project is not ours to install
	if err := os.WriteFile(filepath.Join(e.root, "requirements.yml"), []byte("collections: [community.docker]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.a.OnFilesChanged([]string{"requirements.yml"})
	time.Sleep(time.Second)
	if calls := galaxyCalls(t, logf); len(calls) != 2 {
		t.Fatalf("got %v", calls)
	}
}

func TestRequirementsReady(t *testing.T) {
	for src, ok := range map[string]bool{
		"collections:\n  - community.docker\n":                         true,
		"collections:\n  - name: community.docker\n    version: 3.0\n": true,
		"roles:\n  - src: https://github.com/x/y.git\n":                true,
		"- geerlingguy.docker\n":                                       true,
		"collections:\n  - name: community.docker\n  - name:\n":        false,
		"collections: []\n":                                            false,
		"":                                                             false,
		"collections:\n  - name: [":                                    false,
		"collections: community.docker\n":                              false,
	} {
		if requirementsReady([]byte(src)) != ok {
			t.Errorf("%q: want %v", src, ok)
		}
	}
}
