package files

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type GitFile struct {
	Path   string `json:"path"`
	Status string `json:"status"` // modified | added | deleted | renamed | untracked | conflict
	X      string `json:"x"`      // index status letter
	Y      string `json:"y"`      // worktree status letter
	Orig   string `json:"orig,omitempty"`
}

type GitStatus struct {
	Available bool      `json:"available"`
	Branch    string    `json:"branch,omitempty"`
	Head      string    `json:"head,omitempty"`
	Ahead     int       `json:"ahead,omitempty"`
	Behind    int       `json:"behind,omitempty"`
	Files     []GitFile `json:"files"`
}

func git(ctx context.Context, root string, args ...string) ([]byte, []byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", root}, args...)...)
	// status is read-only: don't take the index lock and block the user's own git
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0", "LC_ALL=C")
	var so, se bytes.Buffer
	cmd.Stdout, cmd.Stderr = &so, &se
	err := cmd.Run()
	code := 0
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		code, err = ee.ExitCode(), nil
	}
	return so.Bytes(), se.Bytes(), code, err
}

// Status runs `git status --porcelain=v2 -z --branch`. Using the git CLI keeps
// behaviour identical to the user's own git.
func Status(ctx context.Context, root string) (*GitStatus, error) {
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return &GitStatus{Files: []GitFile{}}, nil
	}
	out, errOut, code, err := git(ctx, root, "status", "--porcelain=v2", "-z", "--branch", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, fmt.Errorf("git status: %s", strings.TrimSpace(string(errOut)))
	}
	return parseStatus(out), nil
}

func parseStatus(out []byte) *GitStatus {
	st := &GitStatus{Available: true, Files: []GitFile{}}
	recs := strings.Split(string(out), "\x00")
	for i := 0; i < len(recs); i++ {
		r := recs[i]
		if r == "" {
			continue
		}
		switch {
		case strings.HasPrefix(r, "# branch.head "):
			st.Branch = strings.TrimPrefix(r, "# branch.head ")
		case strings.HasPrefix(r, "# branch.oid "):
			if h := strings.TrimPrefix(r, "# branch.oid "); h != "(initial)" {
				st.Head = h
			}
		case strings.HasPrefix(r, "# branch.ab "):
			f := strings.Fields(strings.TrimPrefix(r, "# branch.ab "))
			if len(f) == 2 {
				st.Ahead, _ = strconv.Atoi(strings.TrimPrefix(f[0], "+"))
				st.Behind, _ = strconv.Atoi(strings.TrimPrefix(f[1], "-"))
			}
		case strings.HasPrefix(r, "1 "):
			f := strings.SplitN(r, " ", 9)
			if len(f) == 9 {
				st.Files = append(st.Files, gitFile(f[1], f[8], ""))
			}
		case strings.HasPrefix(r, "2 "):
			f := strings.SplitN(r, " ", 10)
			orig := ""
			if i+1 < len(recs) {
				orig = recs[i+1]
				i++
			}
			if len(f) == 10 {
				st.Files = append(st.Files, gitFile(f[1], f[9], orig))
			}
		case strings.HasPrefix(r, "u "):
			f := strings.SplitN(r, " ", 11)
			if len(f) == 11 {
				g := gitFile(f[1], f[10], "")
				g.Status = "conflict"
				st.Files = append(st.Files, g)
			}
		case strings.HasPrefix(r, "? "):
			st.Files = append(st.Files, GitFile{Path: r[2:], Status: "untracked", X: "?", Y: "?"})
		}
	}
	return st
}

func gitFile(xy, path, orig string) GitFile {
	g := GitFile{Path: path, Orig: orig}
	if len(xy) == 2 {
		g.X, g.Y = xy[:1], xy[1:]
	}
	pick := g.Y
	if pick == "." {
		pick = g.X
	}
	switch pick {
	case "A":
		g.Status = "added"
	case "D":
		g.Status = "deleted"
	case "R", "C":
		g.Status = "renamed"
	default:
		g.Status = "modified"
	}
	return g
}

const maxDiff = 1 << 20

// Diff returns the unified diff of one path against HEAD (staged and unstaged
// together). Untracked files, and repos without commits, diff against nothing.
func Diff(ctx context.Context, root, rel string) (string, error) {
	if _, err := SafePath(root, rel); err != nil {
		return "", err
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return "", errors.New("not a git repository")
	}
	_, _, hasHead, _ := git(ctx, root, "rev-parse", "--verify", "-q", "HEAD")
	tracked := false
	if hasHead == 0 {
		_, _, c, _ := git(ctx, root, "ls-files", "--error-unmatch", "--", rel)
		tracked = c == 0
	}
	var out []byte
	var err error
	if tracked {
		out, _, _, err = git(ctx, root, "diff", "--no-color", "HEAD", "--", rel)
	} else {
		// exit status 1 just means "differences found"
		out, _, _, err = git(ctx, root, "diff", "--no-color", "--no-index", "--", os.DevNull, rel)
	}
	if err != nil {
		return "", err
	}
	if len(out) > maxDiff {
		out = append(out[:maxDiff], []byte("\n… diff truncated …\n")...)
	}
	return string(out), nil
}
