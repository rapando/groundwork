package api

import (
	"github.com/rapando/groundwork/internal/config"
	"net/http"
	"time"
)

func (a *API) listChecks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	diags, err := a.Checks.Diagnostics(q.Get("unit"), q.Get("severity"), q.Get("file"))
	if err != nil {
		writeError(w, 500, "checks_failed", err.Error(), nil)
		return
	}
	counts := map[string]int{"error": 0, "warning": 0, "info": 0}
	for _, d := range diags {
		counts[d.Severity]++
	}
	writeJSON(w, 200, map[string]any{
		"diagnostics": diags, "units": a.Checks.Status(), "counts": counts,
	})
}

type runReq struct {
	Unit  string `json:"unit"`
	Force bool   `json:"force"`
}

// runChecks starts a run and returns immediately; progress arrives over SSE.
func (a *API) runChecks(w http.ResponseWriter, r *http.Request) {
	var req runReq
	if !decode(w, r, &req) {
		return
	}
	if cfg, _ := a.config(); cfg == nil {
		writeError(w, 409, "not_configured", "finish setup first", nil)
		return
	}
	if req.Unit != "" {
		found := false
		for _, u := range a.Checks.Units() {
			if u.ID == req.Unit {
				found = true
			}
		}
		if !found {
			writeError(w, 404, "unknown_unit", "no such unit: "+req.Unit, nil)
			return
		}
	}
	a.Checks.Start(req.Unit, req.Force, 15*time.Minute)
	writeJSON(w, 202, map[string]any{"started": true})
}

type fixReq struct {
	Kind string `json:"kind"`
	File string `json:"file"`
}

// applyFix runs server-side fixes. Edit fixes are applied in the editor buffer
// by the UI (so unsaved work is never overwritten); only fmt runs here.
func (a *API) applyFix(w http.ResponseWriter, r *http.Request) {
	var req fixReq
	if !decode(w, r, &req) {
		return
	}
	if req.Kind != "fmt" {
		writeError(w, 400, "bad_request", "only kind \"fmt\" is applied server-side", nil)
		return
	}
	if err := a.Checks.FormatFile(r.Context(), req.File); err != nil {
		writeError(w, 422, "fix_failed", err.Error(), nil)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

// OnFilesChanged is the watcher callback: tell clients, then re-check.
func (a *API) OnFilesChanged(paths []string) {
	a.Bus.Publish("file.changed", map[string]any{"paths": paths})
	for _, p := range paths {
		if p == config.FileName {
			a.configChanged()
			break
		}
	}
	a.Checks.OnChange(paths)
}
