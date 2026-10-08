package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/rapando/groundwork/internal/api"
	"github.com/rapando/groundwork/internal/app"
	"github.com/rapando/groundwork/internal/events"
	"github.com/rapando/groundwork/internal/files"
	"github.com/rapando/groundwork/internal/server"
	"github.com/rapando/groundwork/internal/store"
)

// Service owns every open project. Each project gets the same machinery the
// single-repo binary had: its own store, event bus, checks, runner, drift
// schedule and file watcher.
type Service struct {
	Home    string
	Version string
	Log     *slog.Logger
	Reg     *Registry

	ctx context.Context

	mu   sync.Mutex
	live map[string]*instance
}

type instance struct {
	p       Project
	err     error // set when the project could not be opened (e.g. its folder is gone)
	api     *api.API
	store   *store.Store
	watcher *files.Watcher
	stop    context.CancelFunc
	handler http.Handler
}

func New(ctx context.Context, home, version string, log *slog.Logger) (*Service, error) {
	reg, err := OpenRegistry(home)
	if err != nil {
		return nil, err
	}
	return &Service{Home: home, Version: version, Log: log, Reg: reg, ctx: ctx, live: map[string]*instance{}}, nil
}

// OpenAll starts every registered project. A project that fails to open is
// kept (and reported) so a missing folder doesn't take the service down.
func (s *Service) OpenAll() {
	for _, p := range s.Reg.List() {
		s.open(p)
	}
}

func (s *Service) open(p Project) *instance {
	s.mu.Lock()
	defer s.mu.Unlock()
	if in, ok := s.live[p.ID]; ok && in.err == nil {
		return in
	}
	in := &instance{p: p}
	s.live[p.ID] = in
	if err := in.start(s.ctx, s.Version, s.Log.With("project", p.ID)); err != nil {
		in.err = err
		s.Log.Warn("project unavailable", "project", p.ID, "path", p.Path, "err", err)
	}
	return in
}

func (in *instance) start(ctx context.Context, version string, log *slog.Logger) error {
	if fi, err := os.Stat(in.p.Path); err != nil || !fi.IsDir() {
		return fmt.Errorf("folder not found: %s", in.p.Path)
	}
	stateDir, err := app.Bootstrap(in.p.Path)
	if err != nil {
		return fmt.Errorf("bootstrap %s: %w", app.StateDir, err)
	}
	st, err := store.Open(filepath.Join(stateDir, "state.db"))
	if err != nil {
		return fmt.Errorf("open store: %w", err)
	}
	bus := events.NewBus()
	a := api.New(in.p.Path, version, bus, st)
	if err := a.Runner.Recover(500, 30*24*time.Hour); err != nil {
		log.Warn("recovering interrupted runs failed", "err", err)
	}
	bg, stop := context.WithCancel(ctx)
	a.Start(bg)
	w, err := files.NewWatcher(in.p.Path, a.Ignore(), 300*time.Millisecond, a.OnFilesChanged, log)
	if err != nil {
		log.Warn("file watching disabled", "err", err)
		w = nil
	}
	r := chi.NewRouter()
	a.Routes(r)
	r.Get("/events", server.SSE(bus))
	r.NotFound(func(w http.ResponseWriter, _ *http.Request) { writeError(w, 404, "not_found", "no such endpoint") })
	in.api, in.store, in.watcher, in.stop, in.handler = a, st, w, stop, r
	return nil
}

// close stops runs gracefully (terraform gets SIGINT so it can release its
// state lock), then the watcher, background work and store, in that order.
func (in *instance) close(ctx context.Context) {
	if in.err != nil || in.api == nil {
		return
	}
	in.api.Runner.Shutdown(ctx)
	if in.watcher != nil {
		in.watcher.Close()
	}
	in.stop()
	in.api.Checks.Close() // after the watcher stops feeding it, before the store closes
	in.store.Close()
}

// Active is the number of runs in progress across all projects.
func (s *Service) Active() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, in := range s.live {
		if in.api != nil {
			n += in.api.Runner.Active()
		}
	}
	return n
}

func (s *Service) Shutdown(ctx context.Context) {
	s.mu.Lock()
	all := make([]*instance, 0, len(s.live))
	for _, in := range s.live {
		all = append(all, in)
	}
	s.live = map[string]*instance{}
	s.mu.Unlock()
	var wg sync.WaitGroup
	for _, in := range all {
		wg.Go(func() { in.close(ctx) })
	}
	wg.Wait()
}

// AddPath imports a local folder. A folder inside a git repository imports
// the repository root, matching what running groundwork there used to do.
func (s *Service) AddPath(path, remote string) (Project, bool, error) {
	path, err := expandHome(strings.TrimSpace(path))
	if err != nil {
		return Project{}, false, err
	}
	if path == "" {
		return Project{}, false, errors.New("path is required")
	}
	fi, err := os.Stat(path)
	if err != nil || !fi.IsDir() {
		return Project{}, false, fmt.Errorf("%s is not a folder", path)
	}
	root, err := app.FindRoot(path)
	if err != nil {
		return Project{}, false, err
	}
	if root, err = filepath.EvalSymlinks(root); err != nil {
		return Project{}, false, err
	}
	if home, _ := os.UserHomeDir(); root == "/" || (home != "" && root == home) {
		return Project{}, false, fmt.Errorf("%s is too broad to import; pick a repository folder", root)
	}
	p, added, err := s.Reg.Add(root, remote)
	if err != nil {
		return Project{}, false, err
	}
	s.open(p)
	return p, added, nil
}

func (s *Service) Remove(id string) error {
	s.mu.Lock()
	in := s.live[id]
	if in != nil && in.api != nil && in.api.Runner.Active() > 0 {
		s.mu.Unlock()
		return errBusy
	}
	delete(s.live, id)
	s.mu.Unlock()
	if in != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		in.close(ctx)
		cancel()
	}
	if _, ok, err := s.Reg.Remove(id); err != nil {
		return err
	} else if !ok {
		return errNotFound
	}
	return nil
}

var (
	errBusy     = errors.New("a run is in progress in this project; wait for it or cancel it first")
	errNotFound = errors.New("no such project")
)

func expandHome(p string) (string, error) {
	if p == "~" || strings.HasPrefix(p, "~/") {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(h, p[1:])
	}
	if p == "" {
		return "", nil
	}
	return filepath.Abs(p)
}

// ---- HTTP ----

// ProjectView is one entry of GET /api/projects.
type ProjectView struct {
	Project
	Status string `json:"status"` // ok | unavailable
	Error  string `json:"error,omitempty"`
	*api.Summary
}

func (s *Service) views() []ProjectView {
	out := []ProjectView{}
	for _, p := range s.Reg.List() {
		v := ProjectView{Project: p, Status: "ok"}
		s.mu.Lock()
		in := s.live[p.ID]
		s.mu.Unlock()
		switch {
		case in == nil:
			v.Status, v.Error = "unavailable", "not open"
		case in.err != nil:
			v.Status, v.Error = "unavailable", in.err.Error()
		default:
			sum := in.api.Summary()
			v.Summary = &sum
		}
		out = append(out, v)
	}
	slices.SortFunc(out, func(a, b ProjectView) int { return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) })
	return out
}

// Routes mounts the service API: the project list and, under /p/{id}, each
// project's own API (the same endpoints the single-repo server had).
func (s *Service) Routes(r chi.Router) {
	r.Get("/service", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"version": s.Version, "home": s.Home, "repos_dir": s.reposDir()})
	})
	r.Get("/projects", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, s.views()) })
	r.Post("/projects", s.addProject)
	r.Post("/projects/{id}/reopen", func(w http.ResponseWriter, r *http.Request) {
		p, ok := s.Reg.Get(chi.URLParam(r, "id"))
		if !ok {
			writeError(w, 404, "not_found", errNotFound.Error())
			return
		}
		if in := s.open(p); in.err != nil {
			writeError(w, 409, "unavailable", in.err.Error())
			return
		}
		writeJSON(w, 200, p)
	})
	r.Delete("/projects/{id}", func(w http.ResponseWriter, r *http.Request) {
		switch err := s.Remove(chi.URLParam(r, "id")); {
		case errors.Is(err, errNotFound):
			writeError(w, 404, "not_found", err.Error())
		case errors.Is(err, errBusy):
			writeError(w, 409, "busy", err.Error())
		case err != nil:
			writeError(w, 500, "remove_failed", err.Error())
		default:
			writeJSON(w, 200, map[string]bool{"ok": true})
		}
	})
	r.Mount("/p/{pid}", http.HandlerFunc(s.dispatch))
}

func (s *Service) dispatch(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	in := s.live[chi.URLParam(r, "pid")]
	s.mu.Unlock()
	switch {
	case in == nil:
		writeError(w, 404, "no_project", errNotFound.Error())
	case in.err != nil:
		writeError(w, 409, "unavailable", in.err.Error())
	default:
		in.handler.ServeHTTP(w, r)
	}
}

type addRequest struct {
	Path string `json:"path"`
	URL  string `json:"url"`
}

func (s *Service) addProject(w http.ResponseWriter, r *http.Request) {
	var req addRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeError(w, 400, "bad_request", "invalid JSON body")
		return
	}
	var (
		p     Project
		added bool
		err   error
	)
	if strings.TrimSpace(req.URL) != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
		defer cancel()
		p, added, err = s.Clone(ctx, req.URL)
	} else {
		p, added, err = s.AddPath(req.Path, "")
	}
	if err != nil {
		writeError(w, 422, "import_failed", err.Error())
		return
	}
	code := 200
	if added {
		code = 201
	}
	writeJSON(w, code, p)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"code": code, "message": msg, "details": nil}})
}
