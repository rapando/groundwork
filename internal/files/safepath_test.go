package files

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafePath(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(root, "link"))
	os.MkdirAll(filepath.Join(root, "a"), 0o755)

	bad := []string{"", "/etc/passwd", "../x", "a/../../x", "link/secret", "link", "a/\x00b"}
	for _, p := range bad {
		if _, err := SafePath(root, p); err == nil {
			t.Errorf("%q should be rejected", p)
		}
	}
	for _, p := range []string{"a/b.tf", "a/../c.tf", "new/dir/file", "./x"} {
		got, err := SafePath(root, p)
		if err != nil {
			t.Errorf("%q: %v", p, err)
			continue
		}
		real, _ := filepath.EvalSymlinks(root)
		if !strings.HasPrefix(got, real) {
			t.Errorf("%q → %q outside root", p, got)
		}
	}
}

func FuzzSafePath(f *testing.F) {
	root := f.TempDir()
	os.MkdirAll(filepath.Join(root, "a", "b"), 0o755)
	os.Symlink(f.TempDir(), filepath.Join(root, "link")) // a symlink pointing out of the repo
	real, _ := filepath.EvalSymlinks(root)
	for _, s := range []string{"a", "../a", "a/../..", "/abs", "a//b", "..\\x", "a/./b", "link/x", "link/../link/x", "a\x00b", "%2e%2e/x", "", "."} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, p string) {
		got, err := SafePath(root, p)
		if err == nil && !within(real, got) {
			t.Fatalf("%q escaped: %q", p, got)
		}
	})
}
