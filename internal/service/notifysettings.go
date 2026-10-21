package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rapando/groundwork/internal/config"
	"github.com/rapando/groundwork/internal/notify"
)

// NotifyPath is the file holding the notification settings for every
// project: a notify: block, as in groundwork.yaml, which a project's own
// block overrides key by key.
func NotifyPath(home string) string { return filepath.Join(home, "notify.yaml") }

// notifyFile caches notify.yaml by its modification time and size, so a
// hand edit applies without a restart.
type notifyFile struct {
	mod  time.Time
	size int64
	n    *config.Notify
	err  error
}

// MachineNotify returns the notification settings for every project (nil
// when notify.yaml is missing, empty or broken; a broken file is logged and
// reported on the settings page).
func (s *Service) MachineNotify() *config.Notify {
	n, _ := s.machineNotify()
	return n
}

func (s *Service) machineNotify() (*config.Notify, error) {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	st, err := os.Stat(NotifyPath(s.Home))
	if err != nil {
		s.notify = notifyFile{}
		return nil, nil
	}
	if c := s.notify; c.mod.Equal(st.ModTime()) && c.size == st.Size() && (c.n != nil || c.err != nil) {
		return c.n, c.err
	}
	b, err := os.ReadFile(NotifyPath(s.Home))
	var n *config.Notify
	if err == nil {
		n, err = config.ParseNotifyFile(b)
	}
	if err != nil {
		err = errors.New(NotifyPath(s.Home) + ": " + err.Error())
		s.Log.Warn("notification settings for every project ignored", "err", err)
	}
	s.notify = notifyFile{mod: st.ModTime(), size: st.Size(), n: n, err: err}
	return n, err
}

func (s *Service) machineSettingsView() map[string]any {
	n, err := s.machineNotify()
	v := map[string]any{"settings": config.ResolveNotify(n), "configured": n != nil, "path": NotifyPath(s.Home)}
	if err != nil {
		v["error"] = err.Error()
	}
	return v
}

func (s *Service) getNotifySettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, 200, s.machineSettingsView())
}

// setNotifySettings writes notify.yaml from the settings page: every key
// explicit, so what was saved is what every project starts from.
func (s *Service) setNotifySettings(w http.ResponseWriter, r *http.Request) {
	var ns config.NotifySettings
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ns); err != nil {
		writeError(w, 400, "bad_request", "invalid JSON body")
		return
	}
	ns.QuietUnder = strings.TrimSpace(ns.QuietUnder)
	ns.Webhook = strings.TrimSpace(ns.Webhook)
	b := ns.Block()
	if probs := config.ValidateNotifyBlock(b, ""); len(probs) > 0 {
		writeJSON(w, 400, map[string]any{"error": map[string]any{"code": "invalid_settings", "message": strings.Join(probs, "; "), "details": probs}})
		return
	}
	out, err := config.RenderNotifyFile(b)
	if err != nil {
		writeError(w, 400, "invalid_settings", err.Error())
		return
	}
	s.notifyWriteMu.Lock()
	defer s.notifyWriteMu.Unlock()
	if err := writeFileAtomic(NotifyPath(s.Home), out); err != nil {
		writeError(w, 500, "write_failed", err.Error())
		return
	}
	writeJSON(w, 200, s.machineSettingsView())
}

// testNotification sends a test by the settings for every project.
func (s *Service) testNotification(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	n := &notify.Notifier{Base: s.MachineNotify, Desktop: notify.Desktop, Webhook: notify.PostWebhook, Project: "groundwork"}
	sent, errs := n.Test(ctx)
	writeJSON(w, 200, map[string]any{"sent": sent, "errors": errs})
}

func writeFileAtomic(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".notify-*.tmp")
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		_ = os.Remove(f.Name())
	}
	return err
}
