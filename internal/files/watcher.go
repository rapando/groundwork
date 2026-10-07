package files

import (
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/rapando/groundwork/internal/workspace"
)

// PollAbove is the file count above which, on kqueue platforms (macOS, BSD),
// the watcher polls instead of using fsnotify. kqueue needs one open file
// descriptor per watched file and macOS allows 10,240 per process, so a large
// repo would starve SQLite, sockets and tool pipes. Linux (inotify) watches
// directories and is never switched.
var PollAbove = 2000

// Watcher reports changed repo-relative paths, debounced and de-duplicated.
type Watcher struct {
	root     string
	ignore   []string
	fw       *fsnotify.Watcher // nil when polling
	every    time.Duration     // polling: minimum gap between sweeps
	debounce time.Duration
	onChange func(paths []string)
	log      *slog.Logger

	mu      sync.Mutex
	pending map[string]struct{}
	timer   *time.Timer
	done    chan struct{}
}

var skipDir = map[string]bool{".git": true, ".terraform": true, "node_modules": true, ".venv": true, ".groundwork": true, "vendor": true}

// NewWatcher watches every non-ignored directory under root. If the OS refuses
// some watches (inotify limits), the rest still work and a warning is logged.
func NewWatcher(root string, ignore []string, debounce time.Duration, onChange func([]string), log *slog.Logger) (*Watcher, error) {
	if log == nil {
		log = slog.Default()
	}
	w := &Watcher{root: root, ignore: ignore, debounce: debounce, onChange: onChange, log: log, every: pollEvery,
		pending: map[string]struct{}{}, done: make(chan struct{})}
	tree, err := workspace.ListTree(root, ignore)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "linux" && len(tree.Files) > PollAbove {
		log.Info("large repository: watching for changes by polling", "files", len(tree.Files))
		go w.poll(snapshot(root, tree))
		return w, nil
	}
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w.fw = fw
	failed := 0
	for _, d := range append([]string{"."}, tree.Dirs...) {
		if err := fw.Add(filepath.Join(root, filepath.FromSlash(d))); err != nil {
			failed++
		}
	}
	if failed > 0 {
		log.Warn("some directories could not be watched; changes there may be missed", "failed", failed)
	}
	go w.loop()
	return w, nil
}

func (w *Watcher) Close() {
	select {
	case <-w.done:
		return
	default:
	}
	close(w.done)
	if w.fw != nil {
		w.fw.Close()
	}
}

type stamp struct {
	mod  time.Time
	size int64
}

func snapshot(root string, tree *workspace.Tree) map[string]stamp {
	m := make(map[string]stamp, len(tree.Files))
	for _, f := range tree.Files {
		if st, err := os.Stat(filepath.Join(root, filepath.FromSlash(f))); err == nil {
			m[f] = stamp{st.ModTime(), st.Size()}
		}
	}
	return m
}

// pollEvery is the minimum gap between polling sweeps; a sweep that takes a
// while stretches the gap (×10) so polling stays under ~10% of one core.
var pollEvery = time.Second

func (w *Watcher) poll(prev map[string]stamp) {
	gap := w.every
	for {
		select {
		case <-w.done:
			return
		case <-time.After(gap):
		}
		start := time.Now()
		tree, err := workspace.ListTree(w.root, w.ignore)
		if err != nil {
			continue
		}
		cur := snapshot(w.root, tree)
		for f, st := range cur {
			if old, ok := prev[f]; !ok || !old.mod.Equal(st.mod) || old.size != st.size {
				if rel, ok := w.rel(filepath.Join(w.root, filepath.FromSlash(f))); ok {
					w.add(rel)
				}
			}
		}
		for f := range prev {
			if _, ok := cur[f]; !ok {
				if rel, ok := w.rel(filepath.Join(w.root, filepath.FromSlash(f))); ok {
					w.add(rel)
				}
			}
		}
		prev = cur
		gap = max(w.every, 10*time.Since(start))
	}
}

func (w *Watcher) rel(p string) (string, bool) {
	r, err := filepath.Rel(w.root, p)
	if err != nil || strings.HasPrefix(r, "..") {
		return "", false
	}
	r = filepath.ToSlash(r)
	for _, seg := range strings.Split(r, "/") {
		if skipDir[seg] {
			return "", false
		}
	}
	// editor temp files and our own atomic-write temp files are noise
	base := filepath.Base(r)
	if strings.HasPrefix(base, ".groundwork-write-") || strings.HasSuffix(base, "~") || strings.HasPrefix(base, ".#") || strings.HasSuffix(base, ".swp") {
		return "", false
	}
	return r, true
}

func (w *Watcher) loop() {
	for {
		select {
		case <-w.done:
			return
		case err, ok := <-w.fw.Errors:
			if !ok {
				return
			}
			w.log.Warn("file watcher error", "err", err)
		case ev, ok := <-w.fw.Events:
			if !ok {
				return
			}
			if ev.Op == fsnotify.Chmod {
				continue
			}
			rel, ok := w.rel(ev.Name)
			if !ok {
				continue
			}
			if ev.Has(fsnotify.Create) {
				if st, err := os.Stat(ev.Name); err == nil && st.IsDir() {
					w.watchNewDir(ev.Name)
					continue
				}
			}
			w.add(rel)
		}
	}
}

// watchNewDir starts watching a new directory and reports files already in it
// (they may have been created before the watch was in place).
func (w *Watcher) watchNewDir(dir string) {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipDir[d.Name()] {
				return filepath.SkipDir
			}
			_ = w.fw.Add(p)
			return nil
		}
		if rel, ok := w.rel(p); ok {
			w.add(rel)
		}
		return nil
	})
}

func (w *Watcher) add(rel string) {
	w.mu.Lock()
	w.pending[rel] = struct{}{}
	if w.timer != nil {
		w.timer.Stop()
	}
	w.timer = time.AfterFunc(w.debounce, w.flush)
	w.mu.Unlock()
}

func (w *Watcher) flush() {
	w.mu.Lock()
	paths := make([]string, 0, len(w.pending))
	for p := range w.pending {
		paths = append(paths, p)
	}
	w.pending = map[string]struct{}{}
	w.mu.Unlock()
	if len(paths) == 0 {
		return
	}
	sort.Strings(paths)
	w.onChange(paths)
}
