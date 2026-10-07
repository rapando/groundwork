package checks

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const lockFile = ".terraform.lock.hcl"

// shadowDir prepares a private working tree for `terraform init/validate` so
// that nothing terraform writes next to the config (notably
// .terraform.lock.hcl) lands in the user's repository.
//
// Along the path from the repo root to the unit, directories are real
// directories inside .groundwork/shadow/<id>/ and every sibling entry is a
// symlink to the real content. Relative module sources and file() references
// therefore still resolve, while the unit's own directory is a real directory
// where terraform can create its lock file. The user's lock file, if any, is
// copied in so provider versions follow it.
func (s *Service) shadowDir(u Unit) (string, error) {
	sum := sha256.Sum256([]byte(u.Path))
	shadowRoot := filepath.Join(s.Root, ".groundwork", "shadow", hex.EncodeToString(sum[:4]))

	var prevLock []byte
	unitShadow := filepath.Join(shadowRoot, filepath.FromSlash(u.Path))
	if b, err := os.ReadFile(filepath.Join(unitShadow, lockFile)); err == nil {
		prevLock = b
	}
	if err := os.RemoveAll(shadowRoot); err != nil {
		return "", err
	}

	var spine []string
	if u.Path != "." && u.Path != "" {
		spine = strings.Split(u.Path, "/")
	}
	realDir, shadow := s.Root, shadowRoot
	for depth := 0; ; depth++ {
		if err := os.MkdirAll(shadow, 0o755); err != nil {
			return "", err
		}
		entries, err := os.ReadDir(realDir)
		if err != nil {
			return "", err
		}
		atUnit := depth == len(spine)
		for _, e := range entries {
			name := e.Name()
			switch {
			case !atUnit && name == spine[depth]:
				continue // the next real directory on the spine
			case depth == 0 && (name == ".git" || name == ".groundwork"):
				continue // never mirror ourselves
			case atUnit && (name == ".terraform" || name == lockFile):
				continue
			}
			if err := os.Symlink(filepath.Join(realDir, name), filepath.Join(shadow, name)); err != nil {
				return "", err
			}
		}
		if atUnit {
			break
		}
		realDir, shadow = filepath.Join(realDir, spine[depth]), filepath.Join(shadow, spine[depth])
	}

	lock := prevLock
	if b, err := os.ReadFile(filepath.Join(s.Root, filepath.FromSlash(u.Path), lockFile)); err == nil {
		lock = b // the user's lock wins
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if lock != nil {
		if err := os.WriteFile(filepath.Join(shadow, lockFile), lock, 0o644); err != nil {
			return "", err
		}
	}
	return shadow, nil
}
