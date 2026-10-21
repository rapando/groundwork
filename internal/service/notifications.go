package service

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"

	"github.com/rapando/groundwork/internal/store"
)

// ProjectNotification is a notification on the service-wide list: the
// project's own, with the project it came from.
type ProjectNotification struct {
	Project     string `json:"project"`
	ProjectName string `json:"project_name"`
	store.Notification
}

// openInstances returns the projects that are open, in registry order.
func (s *Service) openInstances() []*instance {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*instance
	for _, p := range s.Reg.List() {
		if in := s.live[p.ID]; in != nil && in.err == nil && in.api != nil {
			out = append(out, in)
		}
	}
	return out
}

// Notifications merges every open project's notifications, newest first.
func (s *Service) Notifications(limit int) (items []ProjectNotification, unread int) {
	items = []ProjectNotification{}
	for _, in := range s.openInstances() {
		l, err := in.api.Notifications(limit)
		if err != nil {
			continue
		}
		unread += l.Unread
		for _, n := range l.Items {
			items = append(items, ProjectNotification{Project: in.p.ID, ProjectName: in.p.Name, Notification: n})
		}
	}
	slices.SortStableFunc(items, func(a, b ProjectNotification) int {
		if c := b.At.Compare(a.At); c != 0 {
			return c
		}
		return int(b.ID - a.ID)
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items, unread
}

func (s *Service) listNotifications(w http.ResponseWriter, r *http.Request) {
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit <= 0 {
		limit = 50
	}
	items, unread := s.Notifications(min(limit, 500))
	writeJSON(w, 200, map[string]any{"items": items, "unread": unread})
}

// readNotifications marks every notification of one project (or of all
// projects) read and answers with the unread count left across projects.
func (s *Service) readNotifications(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Project string `json:"project"`
		All     bool   `json:"all"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, 400, "bad_request", "invalid JSON body")
		return
	}
	if req.Project == "" && !req.All {
		writeError(w, 400, "bad_request", "name a project or set all")
		return
	}
	found := req.Project == ""
	unread := 0
	for _, in := range s.openInstances() {
		if req.Project == "" || in.p.ID == req.Project {
			found = found || in.p.ID == req.Project
			if _, err := in.api.MarkRead(store.ReadFilter{All: true}); err != nil {
				writeError(w, 500, "read_failed", err.Error())
				return
			}
		}
		unread += in.api.Store.Unread()
	}
	if !found {
		writeError(w, 404, "not_found", errNotFound.Error())
		return
	}
	writeJSON(w, 200, map[string]int{"unread": unread})
}
