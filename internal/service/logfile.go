package service

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"sync"
	"time"
)

// Log rotation: the service owns its log file instead of appending to it
// forever through the login unit's redirection. The current file is
// groundwork.log; on reaching logMaxBytes it becomes groundwork.log.1 and so
// on up to logKeep old files.
const (
	logMaxBytes = 10 << 20
	logKeep     = 3
)

// rotatingWriter is an io.Writer onto a size-capped file. Safe for
// concurrent use (slog handlers call Write from many goroutines).
type rotatingWriter struct {
	path string
	max  int64
	keep int

	mu   sync.Mutex
	f    *os.File
	size int64
}

func openRotatingLog(path string, maxBytes int64, keep int) (*rotatingWriter, error) {
	w := &rotatingWriter{path: path, max: maxBytes, keep: keep}
	if err := w.open(); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *rotatingWriter) open() error {
	f, err := os.OpenFile(w.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.f, w.size = f, st.Size()
	return nil
}

func (w *rotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return 0, errors.New("log closed")
	}
	if w.size+int64(len(p)) > w.max && w.size > 0 {
		if err := w.rotate(); err != nil {
			// keep writing to the current file rather than lose the line
			fmt.Fprintf(os.Stderr, "groundwork: log rotation failed: %v\n", err)
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

// rotate renames groundwork.log.N-1 → .N for N = keep..1, then the current
// file → .1, and opens a fresh file.
func (w *rotatingWriter) rotate() error {
	w.f.Close()
	w.f = nil
	for i := w.keep; i >= 1; i-- {
		src := w.path
		if i > 1 {
			src = w.path + "." + strconv.Itoa(i-1)
		}
		dst := w.path + "." + strconv.Itoa(i)
		if err := os.Rename(src, dst); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = w.open()
			return err
		}
	}
	return w.open()
}

func (w *rotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.f == nil {
		return nil
	}
	err := w.f.Close()
	w.f = nil
	return err
}

// TailLog writes the last n lines of the service log to out and, with
// follow, keeps printing new lines until ctx is done. Rotation is handled:
// when the file shrinks or is replaced, reading restarts from the new file.
func TailLog(ctx context.Context, out io.Writer, home string, n int, follow bool) error {
	path := LogPath(home)
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	lines, err := lastLines(f, n)
	if err != nil {
		return err
	}
	for _, l := range lines {
		fmt.Fprintln(out, l)
	}
	if !follow {
		return nil
	}
	pos, _ := f.Seek(0, io.SeekEnd)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(250 * time.Millisecond):
		}
		st, err := os.Stat(path)
		if err != nil {
			continue // mid-rotation
		}
		if st.Size() < pos { // rotated: a new, smaller file is in place
			f.Close()
			if f, err = os.Open(path); err != nil {
				return err
			}
			pos = 0
		}
		if st.Size() == pos {
			continue
		}
		if _, err := f.Seek(pos, io.SeekStart); err != nil {
			return err
		}
		r := bufio.NewReader(f)
		for {
			l, err := r.ReadString('\n')
			if l != "" && err == nil {
				fmt.Fprint(out, l)
				pos += int64(len(l))
			}
			if err != nil {
				break // a partial last line waits for its newline
			}
		}
	}
}

// lastLines reads a file backwards in blocks and returns its last n lines.
func lastLines(f *os.File, n int) ([]string, error) {
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	const block = 64 << 10
	var buf []byte
	end := st.Size()
	for end > 0 && countNewlines(buf) <= n {
		start := max(end-block, 0)
		chunk := make([]byte, end-start)
		if _, err := f.ReadAt(chunk, start); err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		buf = append(chunk, buf...)
		end = start
	}
	var lines []string
	sc := bufio.NewScanner(bytesReader(buf))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		lines = append(lines, sc.Text())
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}

func bytesReader(b []byte) io.Reader { return bytes.NewReader(b) }

func countNewlines(b []byte) int {
	n := 0
	for _, c := range b {
		if c == '\n' {
			n++
		}
	}
	return n
}
