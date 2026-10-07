package checks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/rapando/groundwork/internal/config"
	"github.com/rapando/groundwork/internal/doctor"
	"github.com/rapando/groundwork/internal/events"
	"github.com/rapando/groundwork/internal/store"
	"github.com/rapando/groundwork/internal/workspace"
)

// ToolStatus is the outcome of the last run of one tool on one unit.
type ToolStatus struct {
	Tool       string     `json:"tool"`
	Status     string     `json:"status"` // idle | running | ok | issues | skipped | failed
	Count      int        `json:"count"`
	Message    string     `json:"message,omitempty"`
	Hint       string     `json:"hint,omitempty"`
	DurationMS int64      `json:"duration_ms,omitempty"`
	RanAt      *time.Time `json:"ran_at,omitempty"`
}

type UnitStatus struct {
	Unit
	Running bool         `json:"running"`
	Results []ToolStatus `json:"results"`
}

type cached struct {
	diags []Diagnostic
	st    ToolStatus
}

// Service owns the check pipeline: it maps files to units, runs the tools,
// caches by content hash, persists diagnostics and publishes events.
type Service struct {
	Root  string
	Cfg   func() *config.Config
	Exec  Exec
	Store *store.Store
	Bus   *events.Bus
	Log   *slog.Logger

	realRoot string
	sem      chan struct{}
	initMu   sync.Mutex // terraform's plugin cache is not safe for concurrent writers
	unitMu   sync.Map   // unit id → *sync.Mutex: one validate (and shadow tree rebuild) per unit at a time

	mu       sync.Mutex
	units    []Unit
	dirty    bool
	loaded   *config.Config
	status   map[string]map[string]ToolStatus
	cache    map[string]cached
	cancels  map[string]context.CancelFunc
	timers   map[string]*time.Timer
	debounce time.Duration

	// background runs (debounced, RunNow, Start) stop with Close
	ctx    context.Context
	stop   context.CancelFunc
	bg     sync.WaitGroup
	closed bool
}

func New(root string, cfg func() *config.Config, ex Exec, st *store.Store, bus *events.Bus, log *slog.Logger) *Service {
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		real = root
	}
	if log == nil {
		log = slog.Default()
	}
	ctx, stop := context.WithCancel(context.Background())
	return &Service{
		ctx: ctx, stop: stop,
		Root: root, Cfg: cfg, Exec: ex, Store: st, Bus: bus, Log: log, realRoot: real,
		sem: make(chan struct{}, 3), dirty: true, debounce: 400 * time.Millisecond,
		status: map[string]map[string]ToolStatus{}, cache: map[string]cached{},
		cancels: map[string]context.CancelFunc{}, timers: map[string]*time.Timer{},
	}
}

// Units returns the current units, re-detecting if the config or files changed.
func (s *Service) Units() []Unit {
	cfg := s.Cfg()
	s.mu.Lock()
	defer s.mu.Unlock()
	if cfg == nil {
		s.units, s.loaded = nil, nil
		return nil
	}
	if s.dirty || s.loaded != cfg {
		rep, err := workspace.Detect(s.Root, cfg.Ignore)
		if err != nil {
			s.Log.Warn("detect for checks failed", "err", err)
			return s.units
		}
		s.units = BuildUnits(cfg, rep)
		s.loaded, s.dirty = cfg, false
		ids := make([]string, len(s.units))
		for i, u := range s.units {
			ids[i] = u.ID
		}
		if s.Store != nil {
			_ = s.Store.DeleteUnitsExcept(ids)
		}
	}
	return s.units
}

func (s *Service) unit(id string) (Unit, bool) {
	for _, u := range s.Units() {
		if u.ID == id {
			return u, true
		}
	}
	return Unit{}, false
}

// Status lists every unit with its per-tool results.
func (s *Service) Status() []UnitStatus {
	units := s.Units()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]UnitStatus, 0, len(units))
	for _, u := range units {
		us := UnitStatus{Unit: u}
		for _, t := range u.Tools {
			st, ok := s.status[u.ID][t]
			if !ok {
				st = ToolStatus{Tool: t, Status: "idle"}
			}
			if st.Status == "running" {
				us.Running = true
			}
			us.Results = append(us.Results, st)
		}
		out = append(out, us)
	}
	return out
}

func (s *Service) setStatus(unit string, st ToolStatus) {
	s.mu.Lock()
	if s.status[unit] == nil {
		s.status[unit] = map[string]ToolStatus{}
	}
	s.status[unit][st.Tool] = st
	s.mu.Unlock()
}

// affected maps changed repo-relative paths to the units that must re-run, and
// marks the unit list stale if files that define units were touched. Returns
// nil when checks are off (no config, or on_save disabled).
func (s *Service) affected(paths []string) []Unit {
	cfg := s.Cfg()
	if cfg == nil {
		return nil
	}
	var rel []string
	for _, p := range paths {
		if p == "" || strings.HasPrefix(p, ".git/") || strings.HasPrefix(p, ".groundwork/") || strings.Contains(p, "/.terraform/") || strings.HasPrefix(p, ".terraform/") {
			continue
		}
		rel = append(rel, p)
		if strings.HasSuffix(p, ".tf") || strings.HasSuffix(p, ".yml") || strings.HasSuffix(p, ".yaml") || path.Base(p) == "ansible.cfg" || path.Base(p) == config.FileName {
			s.mu.Lock()
			s.dirty = true
			s.mu.Unlock()
		}
	}
	if !cfg.Checks.OnSave || len(rel) == 0 {
		return nil
	}
	units := s.Units()
	seen := map[string]bool{}
	var out []Unit
	for _, p := range rel {
		for _, u := range Affected(units, p) {
			if !seen[u.ID] {
				seen[u.ID] = true
				out = append(out, u)
			}
		}
	}
	return out
}

// OnChange is called by the file watcher with changed repo-relative paths;
// runs are debounced so a burst of writes costs one run per unit.
func (s *Service) OnChange(paths []string) {
	for _, u := range s.affected(paths) {
		s.schedule(u.ID)
	}
}

// RunNow re-checks the units affected by paths without debouncing. Used when
// groundwork itself just wrote the file, so there is nothing left to wait for.
// The watcher event that follows hits the content-hash cache.
func (s *Service) RunNow(paths []string) {
	for _, u := range s.affected(paths) {
		s.Start(u.ID, false, 0)
	}
}

// Start runs Run in the background (timeout 0: none). Close cancels and waits for it.
func (s *Service) Start(unitID string, force bool, timeout time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.bg.Add(1)
	go func() {
		defer s.bg.Done()
		ctx := s.ctx
		if timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, timeout)
			defer cancel()
		}
		if err := s.Run(ctx, unitID, force); err != nil && !errors.Is(err, context.Canceled) {
			s.Log.Warn("check run failed", "unit", unitID, "err", err)
		}
	}()
}

func (s *Service) schedule(unit string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	if t := s.timers[unit]; t != nil {
		t.Stop()
	}
	s.timers[unit] = time.AfterFunc(s.debounce, func() { s.Start(unit, false, 0) })
}

// Close cancels pending and in-flight background runs and waits for them, so
// nothing writes to the store or the shadow trees afterwards.
func (s *Service) Close() {
	s.mu.Lock()
	s.closed = true
	for _, t := range s.timers {
		t.Stop()
	}
	s.mu.Unlock()
	s.stop()
	s.bg.Wait()
}

// Run checks one unit, or all units when unitID is empty. force bypasses the
// content-hash cache.
func (s *Service) Run(ctx context.Context, unitID string, force bool) error {
	if unitID == "" {
		var wg sync.WaitGroup
		for _, u := range s.Units() {
			wg.Add(1)
			go func() { defer wg.Done(); _ = s.runUnit(ctx, u, force) }()
		}
		wg.Wait()
		return nil
	}
	u, ok := s.unit(unitID)
	if !ok {
		return fmt.Errorf("unknown unit %q", unitID)
	}
	return s.runUnit(ctx, u, force)
}

func (s *Service) runUnit(parent context.Context, u Unit, force bool) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	s.mu.Lock()
	if prev := s.cancels[u.ID]; prev != nil {
		prev() // a newer change supersedes the in-flight run
	}
	s.cancels[u.ID] = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		// only clear if still ours
		s.cancels[u.ID] = nil
		s.mu.Unlock()
	}()

	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}

	s.Bus.Publish("checks.started", map[string]any{"unit": u.ID})
	hash, herr := s.hashUnit(u)
	var wg sync.WaitGroup
	for _, tool := range u.Tools {
		prev := s.currentStatus(u.ID, tool)
		s.setStatus(u.ID, ToolStatus{Tool: tool, Status: "running", Count: prev.Count})
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.runTool(ctx, u, tool, hash, herr == nil && !force, prev)
		}()
	}
	wg.Wait()
	s.Bus.Publish("checks.updated", map[string]any{"unit": u.ID})
	return ctx.Err()
}

func (s *Service) currentStatus(unit, tool string) ToolStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status[unit][tool]
}

func (s *Service) runTool(ctx context.Context, u Unit, tool, hash string, useCache bool, prev ToolStatus) {
	key := u.ID + "|" + tool + "|" + hash
	if useCache {
		s.mu.Lock()
		c, ok := s.cache[key]
		s.mu.Unlock()
		if ok {
			s.setStatus(u.ID, c.st)
			s.Bus.Publish("checks.updated", map[string]any{"unit": u.ID, "tool": tool})
			return
		}
	}
	start := time.Now()
	diags, st := s.execTool(ctx, u, tool)
	if ctx.Err() != nil { // superseded or cancelled: keep the previous result
		prev.Status = statusAfterCancel(prev)
		s.setStatus(u.ID, prev)
		return
	}
	now := time.Now().UTC()
	st.Tool, st.RanAt, st.DurationMS = tool, &now, time.Since(start).Milliseconds()
	st.Count = len(diags)
	if st.Status == "" {
		st.Status = "ok"
		if len(diags) > 0 {
			st.Status = "issues"
		}
	}
	for i := range diags {
		diags[i].Unit = u.ID
		s.normalise(u, &diags[i])
	}
	if s.Store != nil && st.Status != "failed" {
		rows := make([]store.DiagRow, len(diags))
		for i, d := range diags {
			rows[i] = toRow(d)
		}
		if _, err := s.Store.ReplaceDiagnostics(u.ID, tool, hash, rows); err != nil {
			s.Log.Error("persist diagnostics", "err", err)
		}
	}
	s.mu.Lock()
	if st.Status != "failed" && st.Status != "skipped" {
		s.cache[key] = cached{diags, st}
	}
	s.mu.Unlock()
	s.setStatus(u.ID, st)
	s.Bus.Publish("checks.updated", map[string]any{"unit": u.ID, "tool": tool})
}

func statusAfterCancel(p ToolStatus) string {
	if p.Status == "" || p.Status == "running" {
		return "idle"
	}
	return p.Status
}

func toRow(d Diagnostic) store.DiagRow {
	r := store.DiagRow{
		Tool: d.Tool, Severity: d.Severity, Code: d.Code, Message: d.Message, Detail: d.Detail,
		File: d.File, Line: d.Line, Col: d.Col, EndLine: d.EndLine, EndCol: d.EndCol, Link: d.Link,
	}
	if d.Fix != nil {
		b, _ := json.Marshal(d.Fix)
		r.FixJSON = string(b)
	}
	return r
}

// Diagnostics returns stored findings; empty filters match everything.
func (s *Service) Diagnostics(unit, severity, file string) ([]Diagnostic, error) {
	rows, err := s.Store.ListDiagnostics(unit, severity, file)
	if err != nil {
		return nil, err
	}
	out := make([]Diagnostic, len(rows))
	for i, r := range rows {
		d := Diagnostic{
			ID: r.ID, Unit: r.Unit, Tool: r.Tool, Severity: r.Severity, Code: r.Code, Message: r.Message,
			Detail: r.Detail, File: r.File, Line: r.Line, Col: r.Col, EndLine: r.EndLine, EndCol: r.EndCol, Link: r.Link,
		}
		if r.FixJSON != "" {
			var f QuickFix
			if json.Unmarshal([]byte(r.FixJSON), &f) == nil {
				d.Fix = &f
			}
		}
		out[i] = d
	}
	return out, nil
}

// hashUnit fingerprints everything a run depends on: the unit's files and, for
// Terraform, the module directories it uses.
func (s *Service) hashUnit(u Unit) (string, error) {
	h := sha256.New()
	dirs := append([]string{u.Path}, u.Deps...)
	sort.Strings(dirs)
	total := 0
	for _, d := range dirs {
		base := filepath.Join(s.Root, filepath.FromSlash(d))
		err := filepath.WalkDir(base, func(p string, de fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if de.IsDir() {
				switch de.Name() {
				case ".terraform", ".git", "node_modules", ".groundwork", ".venv":
					return filepath.SkipDir
				}
				return nil
			}
			if !de.Type().IsRegular() {
				return nil
			}
			total++
			if total > 5000 {
				return errors.New("unit too large to fingerprint")
			}
			rel, _ := filepath.Rel(s.Root, p)
			b, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), len(b))
			h.Write(b)
			return nil
		})
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (s *Service) abs(rel string) string { return filepath.Join(s.Root, filepath.FromSlash(rel)) }

func (s *Service) tfBinary() string {
	if cfg := s.Cfg(); cfg != nil && cfg.Terraform.Binary != "" {
		return cfg.Terraform.Binary
	}
	return "terraform"
}

// FormatFile runs the Terraform formatter on one file (the fmt quick fix).
func (s *Service) FormatFile(ctx context.Context, file string) error {
	if !strings.HasSuffix(file, ".tf") && !strings.HasSuffix(file, ".tfvars") {
		return fmt.Errorf("%s is not a Terraform file", file)
	}
	bin := s.tfBinary()
	if _, err := s.Exec.LookPath(bin); err != nil {
		return fmt.Errorf("%s not found on PATH (%s)", bin, doctor.Hint(bin))
	}
	out, err := s.Exec.Run(ctx, Cmd{Dir: s.Root, Argv: []string{bin, "fmt", s.abs(file)}, Timeout: 30 * time.Second})
	if err != nil {
		return err
	}
	if out.ExitCode != 0 {
		return fmt.Errorf("%s fmt failed: %s", bin, firstLine(out.Stderr))
	}
	return nil
}

func firstLine(b []byte) string {
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return "no output"
}

// normalise guarantees every diagnostic points inside the repo. Tools print
// paths in different ways; anything that still escapes the repo (or is
// absolute) is attached to its unit directory rather than shown as nonsense.
func (s *Service) normalise(u Unit, d *Diagnostic) {
	f := d.File
	if filepath.IsAbs(f) {
		if rel, err := filepath.Rel(s.realRoot, f); err == nil && !strings.HasPrefix(rel, "..") {
			d.File = filepath.ToSlash(rel)
			return
		}
		if rel, err := filepath.Rel(s.Root, f); err == nil && !strings.HasPrefix(rel, "..") {
			d.File = filepath.ToSlash(rel)
			return
		}
	} else if f != ".." && !strings.HasPrefix(f, "../") {
		return
	}
	d.File, d.Line, d.Col, d.EndLine, d.EndCol = u.Path, 0, 0, 0, 0
	d.Detail = strings.TrimSpace(d.Detail + "\n(reported at " + f + ")")
}

// lockUnit serialises validate runs of one unit: its shadow tree is rebuilt on
// every run, and two runs doing that at once would corrupt each other.
func (s *Service) lockUnit(id string) func() {
	m, _ := s.unitMu.LoadOrStore(id, &sync.Mutex{})
	mu := m.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}
