package files

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const MaxEditSize = 2 << 20 // 2 MiB

var (
	ErrNotFound  = errors.New("file not found")
	ErrBinary    = errors.New("binary file")
	ErrTooLarge  = errors.New("file too large to edit")
	ErrForbidden = errors.New("path is not editable")
	ErrExists    = errors.New("file already exists")
)

// ConflictError is returned when If-Match no longer matches the file on disk.
type ConflictError struct{ CurrentSHA string }

func (e *ConflictError) Error() string { return "file changed on disk since it was loaded" }

type Content struct {
	Text    string    `json:"content"`
	SHA     string    `json:"sha"`
	ModTime time.Time `json:"mtime"`
	Size    int64     `json:"size"`
}

func SHA(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// editable rejects paths groundwork must never write through the editor:
// VCS internals, its own state, terraform's working data and state files.
func editable(rel string) error {
	rel = filepath.ToSlash(filepath.Clean(rel))
	for _, seg := range strings.Split(rel, "/") {
		switch seg {
		case ".git", ".groundwork", ".terraform":
			return fmt.Errorf("%w: %s", ErrForbidden, rel)
		}
	}
	for _, suf := range []string{".tfstate", ".tfstate.backup"} {
		if strings.HasSuffix(rel, suf) {
			return fmt.Errorf("%w: state files are managed by terraform: %s", ErrForbidden, rel)
		}
	}
	return nil
}

// Read returns a text file's content with its sha. Binary and oversized files
// are refused rather than mangled.
func Read(root, rel string) (*Content, error) {
	full, err := SafePath(root, rel)
	if err != nil {
		return nil, err
	}
	if err := editable(rel); err != nil {
		return nil, err
	}
	st, err := os.Stat(full)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, fmt.Errorf("%s is a directory", rel)
	}
	if st.Size() > MaxEditSize {
		return nil, ErrTooLarge
	}
	b, err := os.ReadFile(full)
	if err != nil {
		return nil, err
	}
	head := b
	if len(head) > 8192 {
		head = head[:8192]
	}
	if strings.IndexByte(string(head), 0) >= 0 || !utf8.Valid(b) {
		return nil, ErrBinary
	}
	return &Content{Text: string(b), SHA: SHA(b), ModTime: st.ModTime(), Size: st.Size()}, nil
}

var writeMu sync.Mutex // serialises check-then-rename; edits are human-paced

// Write replaces a file atomically. ifMatch is the sha the caller last read; a
// mismatch returns *ConflictError. With create, the file must not exist.
func Write(root, rel string, data []byte, ifMatch string, create bool) (string, error) {
	full, err := SafePath(root, rel)
	if err != nil {
		return "", err
	}
	if err := editable(rel); err != nil {
		return "", err
	}
	if len(data) > MaxEditSize {
		return "", ErrTooLarge
	}
	writeMu.Lock()
	defer writeMu.Unlock()

	mode := os.FileMode(0o644)
	cur, err := os.ReadFile(full)
	switch {
	case err == nil:
		if create {
			return "", ErrExists
		}
		if ifMatch != SHA(cur) {
			return "", &ConflictError{CurrentSHA: SHA(cur)}
		}
		if st, serr := os.Stat(full); serr == nil {
			mode = st.Mode().Perm()
		}
	case errors.Is(err, os.ErrNotExist):
		if !create {
			return "", ErrNotFound
		}
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return "", err
		}
	default:
		return "", err
	}

	tmp, err := os.CreateTemp(filepath.Dir(full), ".groundwork-write-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), full); err != nil {
		return "", err
	}
	return SHA(data), nil
}
