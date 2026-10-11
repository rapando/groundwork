package service

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRotatingWriterCapsSizeAndKeepsOldFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "groundwork.log")
	w, err := openRotatingLog(path, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	line := []byte(strings.Repeat("x", 39) + "\n") // 40 bytes: two per file, the third rotates
	for i := 0; i < 9; i++ {
		if _, err := w.Write(line); err != nil {
			t.Fatal(err)
		}
	}
	w.Close()
	for _, f := range []string{"groundwork.log", "groundwork.log.1", "groundwork.log.2"} {
		st, err := os.Stat(filepath.Join(dir, f))
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if st.Size() > 100 {
			t.Fatalf("%s is %d bytes, over the cap", f, st.Size())
		}
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("%s mode %v", f, st.Mode())
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "groundwork.log.3")); err == nil {
		t.Fatal("kept more old files than asked")
	}
	// reopening continues the current file rather than truncating it
	w, _ = openRotatingLog(path, 100, 2)
	w.Write([]byte("more\n"))
	w.Close()
	b, _ := os.ReadFile(path)
	if !strings.HasSuffix(string(b), "x\nmore\n") {
		t.Fatalf("reopen truncated: %q", b)
	}
}

func TestTailLogPrintsLastLinesAndFollowsThroughRotation(t *testing.T) {
	home := t.TempDir()
	w, _ := openRotatingLog(LogPath(home), 1<<20, 2)
	for i := 1; i <= 5; i++ {
		w.Write([]byte("line " + string(rune('0'+i)) + "\n"))
	}
	var out bytes.Buffer
	if err := TailLog(context.Background(), &out, home, 2, false); err != nil {
		t.Fatal(err)
	}
	if out.String() != "line 4\nline 5\n" {
		t.Fatalf("tail: %q", out.String())
	}

	out.Reset()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- TailLog(ctx, &syncBuffer{b: &out}, home, 0, true) }()
	time.Sleep(300 * time.Millisecond)
	w.Write([]byte("line 6\n"))
	time.Sleep(400 * time.Millisecond)
	// rotate by hand: a new, smaller file replaces the old one
	w.Close()
	os.Rename(LogPath(home), LogPath(home)+".1")
	w, _ = openRotatingLog(LogPath(home), 1<<20, 2)
	w.Write([]byte("line 7\n"))
	time.Sleep(600 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "line 6\nline 7\n" {
		t.Fatalf("follow: %q", got)
	}
}

type syncBuffer struct{ b *bytes.Buffer }

func (s *syncBuffer) Write(p []byte) (int, error) { return s.b.Write(p) }
