package scaffold

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/rapando/groundwork/internal/files"
)

type Planned struct {
	File
	Exists bool `json:"exists"`
}

// Plan marks which files already exist under root (dry run; writes nothing).
func Plan(root string, fs []File) ([]Planned, error) {
	out := make([]Planned, 0, len(fs))
	for _, f := range fs {
		full, err := files.SafePath(root, f.Path)
		if err != nil {
			return nil, err
		}
		_, statErr := os.Stat(full)
		out = append(out, Planned{File: f, Exists: statErr == nil})
	}
	return out, nil
}

type Result struct {
	Path   string `json:"path"`
	Status string `json:"status"` // created | overwritten | merged | skipped
}

// Apply writes files under root. Existing files are never overwritten unless
// their path is in overwrite; Merge files get missing lines appended.
func Apply(root string, fs []File, overwrite map[string]bool) ([]Result, error) {
	// Resolve every path first so a bad one aborts before anything is written.
	fulls := make([]string, len(fs))
	for i, f := range fs {
		p, err := files.SafePath(root, f.Path)
		if err != nil {
			return nil, err
		}
		fulls[i] = p
	}
	var res []Result
	for i, f := range fs {
		full := fulls[i]
		mode := os.FileMode(0o644)
		if f.Executable {
			mode = 0o755
		}
		_, statErr := os.Stat(full)
		exists := statErr == nil
		switch {
		case exists && f.Merge:
			changed, err := mergeLines(full, f.Content)
			if err != nil {
				return res, err
			}
			st := "merged"
			if !changed {
				st = "skipped"
			}
			res = append(res, Result{f.Path, st})
		case exists && !overwrite[f.Path]:
			res = append(res, Result{f.Path, "skipped"})
		default:
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				return res, err
			}
			if err := os.WriteFile(full, []byte(f.Content), mode); err != nil {
				return res, err
			}
			if f.Executable {
				_ = os.Chmod(full, mode)
			}
			st := "created"
			if exists {
				st = "overwritten"
			}
			res = append(res, Result{f.Path, st})
		}
	}
	return res, nil
}

func mergeLines(path, add string) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	have := map[string]bool{}
	for _, l := range strings.Split(string(b), "\n") {
		have[strings.TrimSpace(l)] = true
	}
	var missing []string
	for _, l := range strings.Split(add, "\n") {
		t := strings.TrimSpace(l)
		if t != "" && !have[t] {
			missing = append(missing, l)
		}
	}
	if len(missing) == 0 {
		return false, nil
	}
	prefix := ""
	if len(b) > 0 && b[len(b)-1] != '\n' {
		prefix = "\n"
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return false, err
	}
	defer f.Close()
	_, err = io.WriteString(f, prefix+strings.Join(missing, "\n")+"\n")
	return err == nil, err
}

// WriteZip streams the file set as a zip archive.
func WriteZip(w io.Writer, fs []File) error {
	zw := zip.NewWriter(w)
	for _, f := range fs {
		h := &zip.FileHeader{Name: f.Path, Method: zip.Deflate}
		if f.Executable {
			h.SetMode(0o755)
		} else {
			h.SetMode(0o644)
		}
		fw, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(fw, f.Content); err != nil {
			return fmt.Errorf("zip %s: %w", f.Path, err)
		}
	}
	return zw.Close()
}
