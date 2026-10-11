// Package service runs groundwork as one long-lived server that hosts many
// projects (repositories), each with its own checks, runs and watchers.
package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

// Home returns the service's data directory: $GROUNDWORK_HOME, else
// <user config dir>/groundwork (~/Library/Application Support on macOS,
// ~/.config on Linux). Per-project state stays in each repo's .groundwork/.
func Home() (string, error) {
	if h := os.Getenv("GROUNDWORK_HOME"); h != "" {
		return filepath.Abs(h)
	}
	d, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "groundwork"), nil
}

// Project is one imported repository.
type Project struct {
	ID     string    `json:"id"`
	Name   string    `json:"name"`
	Path   string    `json:"path"`
	Remote string    `json:"remote,omitempty"` // set when groundwork cloned it
	Added  time.Time `json:"added"`
}

// RegistryFormat is the layout version of projects.json. A file written by a
// newer groundwork with a higher format is refused rather than misread; this
// build will still read every format up to this one.
const RegistryFormat = 1

// Registry is the persisted project list (<home>/projects.json).
type Registry struct {
	path string
	// Quarantined is set when projects.json was unreadable and moved aside.
	Quarantined string

	mu       sync.Mutex
	projects []Project
}

func OpenRegistry(home string) (*Registry, error) {
	r := &Registry{path: filepath.Join(home, "projects.json")}
	b, err := os.ReadFile(r.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return r, nil
	case err != nil:
		return nil, err
	}
	var doc struct {
		Format   int       `json:"format"` // absent in files written before 0.5
		Projects []Project `json:"projects"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		// a damaged file must not keep the whole service from starting: set
		// it aside (nothing is deleted) and start with no projects; the
		// caller logs Quarantined so the user knows what to restore
		q := fmt.Sprintf("%s.broken-%s", r.path, time.Now().UTC().Format("20060102T150405Z"))
		if rerr := os.Rename(r.path, q); rerr != nil {
			return nil, fmt.Errorf("%s: %w (and could not set it aside: %v)", r.path, err, rerr)
		}
		r.Quarantined = fmt.Sprintf("%s could not be read (%v); it was moved to %s and the project list starts empty. Import your repositories again, or fix and restore the file.", r.path, err, q)
		return r, nil
	}
	if doc.Format > RegistryFormat {
		return nil, fmt.Errorf("%s was written by a newer groundwork (format %d; this build reads up to %d): upgrade groundwork", r.path, doc.Format, RegistryFormat)
	}
	r.projects = doc.Projects
	return r, nil
}

func (r *Registry) List() []Project {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.projects)
}

func (r *Registry) Get(id string) (Project, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.projects {
		if p.ID == id {
			return p, true
		}
	}
	return Project{}, false
}

func (r *Registry) ByPath(path string) (Project, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.projects {
		if p.Path == path {
			return p, true
		}
	}
	return Project{}, false
}

// Add registers path (absolute) and returns the project; a path already
// registered returns the existing entry.
func (r *Registry) Add(path, remote string) (Project, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.projects {
		if p.Path == path {
			return p, false, nil
		}
	}
	p := Project{ID: ProjectID(path), Name: filepath.Base(path), Path: path, Remote: remote, Added: time.Now().UTC()}
	r.projects = append(r.projects, p)
	if err := r.saveLocked(); err != nil {
		r.projects = r.projects[:len(r.projects)-1]
		return Project{}, false, err
	}
	return p, true, nil
}

func (r *Registry) Remove(id string) (Project, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	i := slices.IndexFunc(r.projects, func(p Project) bool { return p.ID == id })
	if i < 0 {
		return Project{}, false, nil
	}
	p := r.projects[i]
	r.projects = slices.Delete(r.projects, i, i+1)
	if err := r.saveLocked(); err != nil {
		r.projects = slices.Insert(r.projects, i, p)
		return Project{}, false, err
	}
	return p, true, nil
}

func (r *Registry) saveLocked() error {
	b, _ := json.MarshalIndent(map[string]any{"format": RegistryFormat, "projects": r.projects}, "", "  ")
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

var slugUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// ProjectID is a URL-safe id that is stable for a path: the directory name
// plus a short hash, so two checkouts named "infra" don't collide.
func ProjectID(path string) string {
	slug := strings.Trim(slugUnsafe.ReplaceAllString(strings.ToLower(filepath.Base(path)), "-"), "-")
	if len(slug) > 32 {
		slug = strings.TrimRight(slug[:32], "-")
	}
	if slug == "" {
		slug = "project"
	}
	sum := sha256.Sum256([]byte(path))
	return slug + "-" + hex.EncodeToString(sum[:3])
}
