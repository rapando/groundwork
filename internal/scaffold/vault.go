package scaffold

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// VaultFile is the ansible-vault password file a setup form names.
type VaultFile struct {
	Path   string `json:"path"`   // as written in groundwork.yaml
	Full   string `json:"-"`      // resolved
	Exists bool   `json:"exists"` // then it is used as it is and no password is asked for
}

// PasswordFile resolves the form's vault password file the way groundwork.yaml
// does (~/, absolute, or relative to ansible/). It must be outside the
// repository so the password can't be committed. nil when the form has none.
func PasswordFile(root string, f *Form) (*VaultFile, error) {
	p := f.VaultPasswordFile
	if p == "" {
		return nil, nil
	}
	full := p
	switch {
	case strings.HasPrefix(p, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		full = filepath.Join(home, p[2:])
	case filepath.IsAbs(p):
	default:
		full = filepath.Join(root, "ansible", filepath.FromSlash(p))
	}
	full = filepath.Clean(full)
	if within(root, full) {
		return nil, FieldError{"vault_password_file", "put the password file outside the repository, such as ~/.vault-pass, so it can't be committed"}
	}
	st, err := os.Stat(full)
	switch {
	case err == nil && st.IsDir():
		return nil, FieldError{"vault_password_file", p + " is a directory"}
	case err == nil:
		return &VaultFile{Path: p, Full: full, Exists: true}, nil
	case errors.Is(err, fs.ErrNotExist):
		return &VaultFile{Path: p, Full: full}, nil
	default:
		return nil, err
	}
}

// CheckPassword is what a vault password may be: one non-empty line.
func CheckPassword(pw string) error {
	if pw == "" || strings.ContainsAny(pw, "\r\n") || len(pw) > 1024 {
		return FieldError{"vault_password", "the password must be one non-empty line"}
	}
	return nil
}

// Create writes pw to the file, on one line, readable only by its owner. It
// never replaces a file: one that appeared since PasswordFile looked is an error.
func (v *VaultFile) Create(pw string) error {
	if err := CheckPassword(pw); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(v.Full), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(v.Full, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("%s already exists; groundwork won't replace it", v.Path)
		}
		return err
	}
	if _, err := f.WriteString(pw + "\n"); err != nil {
		f.Close()
		os.Remove(v.Full)
		return err
	}
	return f.Close()
}

// within reports whether p is root or under it, following symlinks in both
// as far as they exist.
func within(root, p string) bool {
	rel, err := filepath.Rel(realPath(root), realPath(p))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// realPath resolves symlinks in the longest part of p that exists.
func realPath(p string) string {
	p = filepath.Clean(p)
	for rest := ""; ; {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(p, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}
