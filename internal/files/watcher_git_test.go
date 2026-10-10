package files

import (
	"context"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type collector struct {
	mu  sync.Mutex
	got [][]string
}

func (c *collector) add(p []string) { c.mu.Lock(); c.got = append(c.got, p); c.mu.Unlock() }
func (c *collector) all() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, b := range c.got {
		out = append(out, b...)
	}
	return out
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func has(l []string, v string) bool {
	for _, x := range l {
		if x == v {
			return true
		}
	}
	return false
}

func TestWatcherDebouncesFiltersAndFollowsNewDirs(t *testing.T) {
	t.Run("fsnotify", func(t *testing.T) { testWatcher(t) })
	t.Run("polling", func(t *testing.T) { // what large repos get on macOS
		if runtime.GOOS == "linux" {
			t.Skip("linux always uses inotify")
		}
		defer func(a int, e time.Duration) { PollAbove, pollEvery = a, e }(PollAbove, pollEvery)
		PollAbove, pollEvery = -1, 20*time.Millisecond
		testWatcher(t)
	})
}

func testWatcher(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "terraform"), 0o755)
	os.MkdirAll(filepath.Join(root, ".git"), 0o755)
	col := &collector{}
	// The window is wide next to the burst below: on a loaded CI runner a
	// goroutine can stall for tens of milliseconds between two writes, which
	// would rightly split a narrow window into two batches.
	const debounce = 300 * time.Millisecond
	w, err := NewWatcher(root, nil, debounce, col.add, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// a burst of writes to one file becomes one batch with one entry
	f := filepath.Join(root, "terraform/main.tf")
	for i := 0; i < 5; i++ {
		os.WriteFile(f, []byte{byte('a' + i)}, 0o644)
	}
	waitFor(t, "main.tf event", func() bool { return has(col.all(), "terraform/main.tf") })
	time.Sleep(debounce + 100*time.Millisecond) // a second batch would have arrived by now
	n := 0
	for _, p := range col.all() {
		if p == "terraform/main.tf" {
			n++
		}
	}
	if n != 1 || len(col.got) != 1 {
		t.Fatalf("burst should coalesce into one batch/one path, got %v", col.got)
	}

	// noise is ignored
	os.WriteFile(filepath.Join(root, ".git/index"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, "terraform/.main.tf.swp"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, "terraform/.groundwork-write-123"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(root, "terraform/backup.tf~"), []byte("x"), 0o644)

	// a brand-new directory is watched, and files already inside it are reported
	nd := filepath.Join(root, "terraform/modules/net")
	os.MkdirAll(nd, 0o755)
	os.WriteFile(filepath.Join(nd, "a.tf"), []byte("x"), 0o644)
	waitFor(t, "new dir file", func() bool { return has(col.all(), "terraform/modules/net/a.tf") })
	os.WriteFile(filepath.Join(nd, "b.tf"), []byte("x"), 0o644) // after the watch exists
	waitFor(t, "file in new dir", func() bool { return has(col.all(), "terraform/modules/net/b.tf") })

	// deletes are reported too
	os.Remove(f)
	waitFor(t, "delete", func() bool {
		c := 0
		for _, p := range col.all() {
			if p == "terraform/main.tf" {
				c++
			}
		}
		return c >= 2
	})
	for _, p := range col.all() {
		if strings.HasPrefix(p, ".git/") || strings.HasSuffix(p, ".swp") || strings.Contains(p, ".groundwork-write-") || strings.HasSuffix(p, "~") {
			t.Errorf("noise leaked: %s", p)
		}
	}
}

func TestParseStatus(t *testing.T) {
	out := "# branch.oid 1234abcd\x00# branch.head main\x00# branch.upstream origin/main\x00# branch.ab +2 -1\x00" +
		"1 .M N... 100644 100644 100644 aaa bbb terraform/main.tf\x00" +
		"1 A. N... 000000 100644 100644 000 bbb new file with spaces.tf\x00" +
		"1 .D N... 100644 100644 000000 aaa bbb gone.tf\x00" +
		"2 R. N... 100644 100644 100644 aaa bbb R100 renamed.tf\x00old name.tf\x00" +
		"u UU N... 100644 100644 100644 100644 a b c conflicted.tf\x00" +
		"? untracked dir/x.tf\x00"
	st := parseStatus([]byte(out))
	if st.Branch != "main" || st.Head != "1234abcd" || st.Ahead != 2 || st.Behind != 1 {
		t.Fatalf("%+v", st)
	}
	want := []GitFile{
		{Path: "terraform/main.tf", Status: "modified", X: ".", Y: "M"},
		{Path: "new file with spaces.tf", Status: "added", X: "A", Y: "."},
		{Path: "gone.tf", Status: "deleted", X: ".", Y: "D"},
		{Path: "renamed.tf", Status: "renamed", X: "R", Y: ".", Orig: "old name.tf"},
		{Path: "conflicted.tf", Status: "conflict", X: "U", Y: "U"},
		{Path: "untracked dir/x.tf", Status: "untracked", X: "?", Y: "?"},
	}
	if !reflect.DeepEqual(st.Files, want) {
		t.Fatalf("got  %+v\nwant %+v", st.Files, want)
	}
}

func gitRepo(t *testing.T) string {
	// git commit spawns detached auto-maintenance that can still be writing into .git
	// when the temp dir is removed; keep the test hermetic.
	t.Setenv("GIT_CONFIG_COUNT", "2")
	t.Setenv("GIT_CONFIG_KEY_0", "gc.auto")
	t.Setenv("GIT_CONFIG_VALUE_0", "0")
	t.Setenv("GIT_CONFIG_KEY_1", "maintenance.auto")
	t.Setenv("GIT_CONFIG_VALUE_1", "false")

	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git missing")
	}
	t.Setenv("GIT_AUTHOR_NAME", "t")
	t.Setenv("GIT_AUTHOR_EMAIL", "t@e.com")
	t.Setenv("GIT_COMMITTER_NAME", "t")
	t.Setenv("GIT_COMMITTER_EMAIL", "t@e.com")
	root := t.TempDir()
	run := func(a ...string) {
		if out, err := exec.Command("git", append([]string{"-C", root}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", a, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	return root
}

func TestStatusAndDiffAgainstRealGit(t *testing.T) {
	root := gitRepo(t)
	run := func(a ...string) {
		if out, err := exec.Command("git", append([]string{"-C", root}, a...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", a, err, out)
		}
	}
	ctx := context.Background()

	// no commits yet: untracked file still diffs as all-added
	os.WriteFile(filepath.Join(root, "a.tf"), []byte("one\ntwo\n"), 0o644)
	d, err := Diff(ctx, root, "a.tf")
	if err != nil || !strings.Contains(d, "+one") || !strings.Contains(d, "+two") {
		t.Fatalf("%v\n%s", err, d)
	}
	st, _ := Status(ctx, root)
	if !st.Available || st.Branch != "main" || len(st.Files) != 1 || st.Files[0].Status != "untracked" {
		t.Fatalf("%+v", st)
	}

	run("add", ".")
	run("commit", "-qm", "init")
	os.WriteFile(filepath.Join(root, "a.tf"), []byte("one\nTWO\n"), 0o644)
	os.WriteFile(filepath.Join(root, "b.tf"), []byte("new\n"), 0o644)
	st, _ = Status(ctx, root)
	if st.Head == "" || len(st.Files) != 2 {
		t.Fatalf("%+v", st)
	}
	d, _ = Diff(ctx, root, "a.tf")
	if !strings.Contains(d, "-two") || !strings.Contains(d, "+TWO") {
		t.Fatalf("tracked diff:\n%s", d)
	}
	d, _ = Diff(ctx, root, "b.tf")
	if !strings.Contains(d, "+new") {
		t.Fatalf("untracked diff:\n%s", d)
	}
	if _, err := Diff(ctx, root, "../etc/passwd"); err == nil {
		t.Fatal("diff must go through SafePath")
	}

	// status must not leave an index.lock or otherwise block the user's own git
	Status(ctx, root)
	if _, err := os.Stat(filepath.Join(root, ".git/index.lock")); err == nil {
		t.Fatal("index.lock left behind")
	}
}

func TestStatusOutsideGit(t *testing.T) {
	st, err := Status(context.Background(), t.TempDir())
	if err != nil || st.Available || st.Files == nil {
		t.Fatalf("%+v %v", st, err)
	}
	if _, err := Diff(context.Background(), t.TempDir(), "x"); err == nil {
		t.Fatal("expected error outside git")
	}
}
