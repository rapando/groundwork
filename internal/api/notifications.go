package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rapando/groundwork/v2/internal/config"
	"github.com/rapando/groundwork/v2/internal/drift"
	"github.com/rapando/groundwork/v2/internal/files"
	"github.com/rapando/groundwork/v2/internal/notify"
	"github.com/rapando/groundwork/v2/internal/store"
)

// causeFor links a failed run to the Troubleshoot issue it raised: the
// open one first, else the most recently seen.
func (a *API) causeFor(runID int64) *store.Cause {
	is, err := a.Store.IssuesForRun(runID)
	if err != nil || len(is) == 0 {
		return nil
	}
	pick := is[0]
	for _, i := range is {
		if i.Status == store.IssueOpen && i.Source == "run" {
			pick = i
			break
		}
	}
	v := a.Issues.View(pick)
	c := &store.Cause{IssueID: pick.ID, RuleID: pick.RuleID, Title: v.Title}
	if len(v.Steps) > 0 {
		if act := v.Steps[0].Action; act != nil && act.Label != "" {
			c.Action = act.Label
		} else {
			c.Action = v.Steps[0].Title
		}
	}
	return c
}

// driftNotice is the drift schedule's delivery: the effective drift
// channels, with the desktop notification left to an open console.
func (a *API) driftNotice(ctx context.Context, _ config.Drift, n drift.Notice) error {
	n.Project = a.Notifier.Project
	s := a.NotifySettings()
	return a.Notifier.External(ctx, s.Drift, "drift in "+n.Env, n.Text, n)
}

// NotificationsList is GET /notifications.
type NotificationsList struct {
	Items  []store.Notification `json:"items"`
	Unread int                  `json:"unread"`
}

// Notifications returns the newest notifications and the unread count.
func (a *API) Notifications(limit int) (NotificationsList, error) {
	items, err := a.Store.ListNotifications(limit)
	if err != nil {
		return NotificationsList{}, err
	}
	return NotificationsList{Items: items, Unread: a.Store.Unread()}, nil
}

func limitParam(r *http.Request) int {
	n, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || n <= 0 {
		return 50
	}
	return min(n, 500)
}

func (a *API) listNotifications(w http.ResponseWriter, r *http.Request) {
	l, err := a.Notifications(limitParam(r))
	if err != nil {
		writeError(w, 500, "notifications_failed", err.Error(), nil)
		return
	}
	writeJSON(w, 200, l)
}

// MarkRead marks notifications read and tells the console the new count.
func (a *API) MarkRead(f store.ReadFilter) (int, error) {
	n, err := a.Store.MarkNotificationsRead(f)
	if err != nil {
		return 0, err
	}
	unread := a.Store.Unread()
	if n > 0 {
		a.Bus.Publish(notify.EventUpdated, map[string]int{"unread": unread})
	}
	return unread, nil
}

func (a *API) readNotifications(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs   []int64 `json:"ids"`
		RunID int64   `json:"run_id"`
		All   bool    `json:"all"`
	}
	if !decode(w, r, &req) {
		return
	}
	if len(req.IDs) > 1000 {
		writeError(w, 400, "bad_request", "too many ids", nil)
		return
	}
	unread, err := a.MarkRead(store.ReadFilter{IDs: req.IDs, RunID: req.RunID, All: req.All})
	if err != nil {
		writeError(w, 500, "read_failed", err.Error(), nil)
		return
	}
	writeJSON(w, 200, map[string]int{"unread": unread})
}

func (a *API) baseNotify() *config.Notify {
	if a.MachineNotify == nil {
		return nil
	}
	return a.MachineNotify()
}

// NotifySettings are the project's effective notification settings: its
// notify: block over the settings for every project.
func (a *API) NotifySettings() config.NotifySettings {
	return a.Config().NotifySettingsOver(a.baseNotify())
}

// settingsView is GET /notifications/settings: the effective settings, the
// project's overrides (null or "" where it follows every project's) and the
// settings for every project they apply over.
func (a *API) settingsView() map[string]any {
	c := a.Config()
	base := a.baseNotify()
	return map[string]any{"settings": c.NotifySettingsOver(base), "configured": c.NotifyConfigured(),
		"overrides": c.NotifyOverrides(), "base": config.ResolveNotify(base), "shared": a.MachineNotify != nil}
}

func (a *API) notifySettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, a.settingsView())
}

// setNotifySettings writes the project's overrides as the notify: block of
// groundwork.yaml (removing it when nothing is overridden), leaving the rest
// of the file (comments included) as it was. A key that is null, or "" for
// quiet_under and webhook, follows the settings for every project.
func (a *API) setNotifySettings(w http.ResponseWriter, r *http.Request) {
	var s config.Notify
	if !decode(w, r, &s) {
		return
	}
	s.QuietUnder = strings.TrimSpace(s.QuietUnder)
	s.Webhook = strings.TrimSpace(s.Webhook)
	cfg, cfgErr := a.config()
	if cfg == nil {
		msg := "set up groundwork.yaml first"
		if cfgErr != nil {
			msg = "groundwork.yaml has problems; fix them first: " + cfgErr.Error()
		}
		writeError(w, 409, "not_configured", msg, nil)
		return
	}
	if probs := cfg.ValidateNotifyOverrides(s, a.baseNotify()); len(probs) > 0 {
		writeError(w, 400, "invalid_settings", strings.Join(probs, "; "), probs)
		return
	}
	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()
	p := filepath.Join(a.Root, config.FileName)
	cur, err := os.ReadFile(p)
	if err != nil {
		writeError(w, 500, "read_failed", err.Error(), nil)
		return
	}
	out, err := config.SetNotifyBlock(cur, s)
	if err != nil {
		writeError(w, 400, "invalid_settings", err.Error(), nil)
		return
	}
	if _, err := config.Parse(out, config.FileName); err != nil {
		var ve *config.ValidationError
		if errors.As(err, &ve) {
			writeError(w, 400, "invalid_settings", err.Error(), ve.Problems)
			return
		}
		writeError(w, 400, "invalid_settings", err.Error(), nil)
		return
	}
	if _, err := files.Write(a.Root, config.FileName, out, files.SHA(cur), false); err != nil {
		writeError(w, 500, "write_failed", err.Error(), nil)
		return
	}
	a.configChanged()
	writeJSON(w, 200, a.settingsView())
}

func (a *API) testNotification(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	sent, errs := a.Notifier.Test(ctx)
	writeJSON(w, 200, map[string]any{"sent": sent, "errors": errs})
}

func (a *API) getRunNotify(w http.ResponseWriter, r *http.Request) {
	id, ok := runID(w, r)
	if !ok {
		return
	}
	if _, err := a.Store.GetRun(id); err != nil {
		runError(w, err)
		return
	}
	writeJSON(w, 200, map[string]string{"mode": a.Notifier.Mode(id)})
}

func (a *API) setRunNotify(w http.ResponseWriter, r *http.Request) {
	id, ok := runID(w, r)
	if !ok {
		return
	}
	var req struct {
		Mode string `json:"mode"`
	}
	if !decode(w, r, &req) {
		return
	}
	if _, err := a.Store.GetRun(id); err != nil {
		runError(w, err)
		return
	}
	if err := a.Notifier.SetMode(id, req.Mode); err != nil {
		code := 500
		if errors.Is(err, notify.ErrBadMode) {
			code = 400
		}
		writeError(w, code, "bad_request", err.Error(), nil)
		return
	}
	writeJSON(w, 200, map[string]string{"mode": req.Mode})
}
