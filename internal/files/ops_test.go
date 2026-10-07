package files

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadWriteConflict(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "a.tf"), []byte("one\n"), 0o640)

	c, err := Read(root, "a.tf")
	if err != nil || c.Text != "one\n" || c.SHA != SHA([]byte("one\n")) {
		t.Fatalf("%+v %v", c, err)
	}

	// stale If-Match → conflict carrying the current sha, file untouched
	_, err = Write(root, "a.tf", []byte("mine\n"), "stale", false)
	var ce *ConflictError
	if !errors.As(err, &ce) || ce.CurrentSHA != c.SHA {
		t.Fatalf("want conflict, got %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "a.tf")); string(b) != "one\n" {
		t.Fatal("conflicting write modified the file")
	}

	sha, err := Write(root, "a.tf", []byte("two\n"), c.SHA, false)
	if err != nil || sha != SHA([]byte("two\n")) {
		t.Fatalf("%v", err)
	}
	if st, _ := os.Stat(filepath.Join(root, "a.tf")); st.Mode().Perm() != 0o640 {
		t.Fatalf("mode not preserved: %v", st.Mode())
	}
	// no temp files left behind
	entries, _ := os.ReadDir(root)
	if len(entries) != 1 {
		t.Fatalf("leftovers: %v", entries)
	}
	// the old sha no longer works
	if _, err := Write(root, "a.tf", []byte("x"), c.SHA, false); err == nil {
		t.Fatal("second write with the old sha must conflict")
	}
}

func TestCreateAndMissing(t *testing.T) {
	root := t.TempDir()
	if _, err := Write(root, "new/dir/x.tf", []byte("hi"), "", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update of missing file: %v", err)
	}
	if _, err := Write(root, "new/dir/x.tf", []byte("hi"), "", true); err != nil {
		t.Fatal(err)
	}
	if _, err := Write(root, "new/dir/x.tf", []byte("hi"), "", true); !errors.Is(err, ErrExists) {
		t.Fatalf("create over existing: %v", err)
	}
	if _, err := Read(root, "nope.tf"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

func TestRefusals(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "bin"), []byte{0, 1, 2, 3}, 0o644)
	os.WriteFile(filepath.Join(root, "latin1.txt"), []byte{0xe9, 0xe8}, 0o644)
	os.WriteFile(filepath.Join(root, "big.txt"), []byte(strings.Repeat("a", MaxEditSize+1)), 0o644)
	if _, err := Read(root, "bin"); !errors.Is(err, ErrBinary) {
		t.Fatalf("binary: %v", err)
	}
	if _, err := Read(root, "latin1.txt"); !errors.Is(err, ErrBinary) {
		t.Fatalf("invalid utf8 would be corrupted by a round-trip: %v", err)
	}
	if _, err := Read(root, "big.txt"); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("large: %v", err)
	}
	for _, p := range []string{".git/config", ".groundwork/state.db", "x/.terraform/y.tf", "terraform.tfstate", "a/b.tfstate.backup"} {
		if _, err := Read(root, p); !errors.Is(err, ErrForbidden) {
			t.Errorf("read %s: %v", p, err)
		}
		if _, err := Write(root, p, []byte("x"), "", true); !errors.Is(err, ErrForbidden) {
			t.Errorf("write %s: %v", p, err)
		}
	}
	if _, err := Write(root, "../escape", []byte("x"), "", true); err == nil {
		t.Fatal("path escape")
	}
	if _, err := Write(root, "big2.txt", []byte(strings.Repeat("a", MaxEditSize+1)), "", true); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized write: %v", err)
	}
}

func TestConcurrentWritesOnlyOneWins(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "f"), []byte("0"), 0o644)
	sha := SHA([]byte("0"))
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		go func() {
			_, err := Write(root, "f", []byte{byte('a' + i)}, sha, false)
			results <- err
		}()
	}
	wins := 0
	for i := 0; i < 8; i++ {
		if err := <-results; err == nil {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("%d writers succeeded with the same If-Match; exactly 1 must", wins)
	}
}

func TestUnifiedDiff(t *testing.T) {
	a := "1\n2\n3\n4\n5\n6\n7\n8\n"
	b := "1\n2\n3\n4\nfive\n6\n7\n8\n"
	d := UnifiedDiff("x.tf", a, b)
	want := "--- a/x.tf\n+++ b/x.tf\n@@ -2,7 +2,7 @@\n 2\n 3\n 4\n-5\n+five\n 6\n 7\n 8\n"
	if d != want {
		t.Fatalf("got\n%s\nwant\n%s", d, want)
	}
	if UnifiedDiff("x", a, a) != "" {
		t.Fatal("no change → empty")
	}
	if d := UnifiedDiff("x", "a\n", "a\nb\n"); !strings.Contains(d, "+b") {
		t.Fatalf("%s", d)
	}
}
