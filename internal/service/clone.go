package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Remote URLs groundwork will clone: https, ssh:// and scp-style git@host:path.
// Other transports (ext::, file://, local paths) are refused; a local
// repository is imported by path instead.
var remoteRe = regexp.MustCompile(`^(https://[^\s]+|ssh://[^\s]+|[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[^\s]+)$`)

func (s *Service) reposDir() string { return filepath.Join(s.Home, "repos") }

// Clone clones url into <home>/repos/<name> with the user's own git, so SSH
// keys and credential helpers work as they do in a terminal, then imports it.
// A URL that was already cloned imports the existing checkout.
func (s *Service) Clone(ctx context.Context, url string) (Project, bool, error) {
	url = strings.TrimSpace(url)
	if !remoteRe.MatchString(url) {
		return Project{}, false, errors.New("expected an https://, ssh:// or git@host:path repository URL")
	}
	return s.cloneFrom(ctx, url, url)
}

// cloneFrom clones source and records remote as where it came from; tests
// clone a local repository under a network-looking name.
func (s *Service) cloneFrom(ctx context.Context, remote, source string) (Project, bool, error) {
	for _, p := range s.Reg.List() {
		if p.Remote == remote {
			return p, false, nil
		}
	}
	if err := os.MkdirAll(s.reposDir(), 0o755); err != nil {
		return Project{}, false, err
	}
	dest := uniqueDir(s.reposDir(), repoName(remote))
	cmd := exec.CommandContext(ctx, "git", "-c", "protocol.ext.allow=never", "clone", "--quiet", "--", source, dest)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0") // a service has no terminal to prompt on
	if os.Getenv("GIT_SSH_COMMAND") == "" {
		cmd.Env = append(cmd.Env, "GIT_SSH_COMMAND=ssh -o BatchMode=yes")
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		_ = os.RemoveAll(dest)
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return Project{}, false, fmt.Errorf("git clone failed: %s", msg)
	}
	return s.AddPath(dest, remote)
}

// repoName is the last path segment of a remote URL without ".git".
func repoName(url string) string {
	url = strings.TrimSuffix(strings.TrimRight(url, "/"), ".git")
	if i := strings.LastIndexAny(url, "/:"); i >= 0 {
		url = url[i+1:]
	}
	url = strings.Trim(slugUnsafe.ReplaceAllString(strings.ToLower(url), "-"), "-")
	if url == "" {
		return "repo"
	}
	return url
}

func uniqueDir(parent, name string) string {
	dest := filepath.Join(parent, name)
	for i := 2; ; i++ {
		if _, err := os.Lstat(dest); errors.Is(err, os.ErrNotExist) {
			return dest
		}
		dest = filepath.Join(parent, name+"-"+strconv.Itoa(i))
	}
}
