package store

import (
	"database/sql"
	"encoding/json"
	"strings"
	"time"
)

// Notification events.
const (
	EventFailed    = "failed"
	EventApproval  = "approval"
	EventSucceeded = "succeeded"
	EventCancelled = "cancelled"
	EventDrift     = "drift"
)

// NotificationMaxAge is how long notifications are kept.
const NotificationMaxAge = 30 * 24 * time.Hour

// Cause is the Troubleshoot issue a failure matched.
type Cause struct {
	IssueID int64  `json:"issue_id"`
	RuleID  string `json:"rule_id"`
	Title   string `json:"title"`
	Action  string `json:"action,omitempty"` // label of the issue's first step
}

// PlanCounts summarises a plan waiting for approval.
type PlanCounts struct {
	Create  int `json:"create"`
	Update  int `json:"update"`
	Replace int `json:"replace"`
	Delete  int `json:"delete"`
	Danger  int `json:"danger"`
}

// Notification is one event about a run, as the console shows it.
type Notification struct {
	ID         int64       `json:"id"`
	Event      string      `json:"event"`
	RunID      int64       `json:"run_id"`
	Kind       string      `json:"kind"`
	Stage      string      `json:"stage"`
	Label      string      `json:"label"`
	Root       string      `json:"root"`
	Env        string      `json:"env"`
	Title      string      `json:"title"`
	Text       string      `json:"text"`
	Cause      *Cause      `json:"cause,omitempty"`
	Plan       *PlanCounts `json:"plan,omitempty"`
	DriftCount int         `json:"drift_count,omitempty"`
	DurationS  int64       `json:"duration_s"`
	At         time.Time   `json:"at"`
	ReadAt     *time.Time  `json:"read_at,omitempty"`
	Toast      bool        `json:"toast"`
	Desktop    bool        `json:"desktop"`
}

func marshalOpt(v any, isNil bool) string {
	if isNil {
		return ""
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// AddNotification records n (setting its ID) unless one already exists for
// the same run and event. ok is false for such a duplicate.
func (s *Store) AddNotification(n *Notification) (ok bool, err error) {
	if n.At.IsZero() {
		n.At = time.Now()
	}
	n.At = n.At.UTC()
	res, err := s.DB.Exec(`INSERT OR IGNORE INTO notifications(event, run_id, kind, stage, label, root, env, title, text, cause_json, plan_json, drift_count, duration_s, at, toast, desktop)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		n.Event, n.RunID, n.Kind, n.Stage, n.Label, n.Root, n.Env, n.Title, n.Text,
		marshalOpt(n.Cause, n.Cause == nil), marshalOpt(n.Plan, n.Plan == nil), n.DriftCount, n.DurationS, n.At, n.Toast, n.Desktop)
	if err != nil {
		return false, err
	}
	if c, _ := res.RowsAffected(); c == 0 {
		return false, nil
	}
	n.ID, _ = res.LastInsertId()
	return true, nil
}

// HasNotification reports whether (run, event) was already recorded.
func (s *Store) HasNotification(runID int64, event string) bool {
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM notifications WHERE run_id = ? AND event = ?`, runID, event).Scan(&n)
	return n > 0
}

const notificationCols = `id, event, run_id, kind, stage, label, root, env, title, text, cause_json, plan_json, drift_count, duration_s, at, read_at, toast, desktop`

func scanNotification(sc interface{ Scan(...any) error }) (Notification, error) {
	var n Notification
	var cause, plan string
	var read sql.NullTime
	if err := sc.Scan(&n.ID, &n.Event, &n.RunID, &n.Kind, &n.Stage, &n.Label, &n.Root, &n.Env, &n.Title, &n.Text,
		&cause, &plan, &n.DriftCount, &n.DurationS, &n.At, &read, &n.Toast, &n.Desktop); err != nil {
		return n, err
	}
	if cause != "" {
		n.Cause = &Cause{}
		_ = json.Unmarshal([]byte(cause), n.Cause)
	}
	if plan != "" {
		n.Plan = &PlanCounts{}
		_ = json.Unmarshal([]byte(plan), n.Plan)
	}
	if read.Valid {
		t := read.Time
		n.ReadAt = &t
	}
	return n, nil
}

// GetNotification returns one notification.
func (s *Store) GetNotification(id int64) (*Notification, error) {
	n, err := scanNotification(s.DB.QueryRow(`SELECT `+notificationCols+` FROM notifications WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	return &n, nil
}

// ListNotifications returns the newest notifications first.
func (s *Store) ListNotifications(limit int) ([]Notification, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.DB.Query(`SELECT `+notificationCols+` FROM notifications ORDER BY at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Notification{}
	for rows.Next() {
		n, err := scanNotification(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// UnreadCounts returns the number of unread notifications and how many of
// them are failures.
func (s *Store) UnreadCounts() (unread, failed int) {
	_ = s.DB.QueryRow(`SELECT COUNT(*), COALESCE(SUM(event = ?), 0) FROM notifications WHERE read_at IS NULL`, EventFailed).Scan(&unread, &failed)
	return unread, failed
}

// Unread is the number of unread notifications.
func (s *Store) Unread() int {
	n, _ := s.UnreadCounts()
	return n
}

// ReadFilter picks notifications to mark read: by id, by run, or all.
type ReadFilter struct {
	IDs   []int64
	RunID int64
	All   bool
}

// MarkNotificationsRead marks the selected unread notifications read and
// returns how many changed.
func (s *Store) MarkNotificationsRead(f ReadFilter) (int, error) {
	now := time.Now().UTC()
	var res sql.Result
	var err error
	switch {
	case f.All:
		res, err = s.DB.Exec(`UPDATE notifications SET read_at = ? WHERE read_at IS NULL`, now)
	case f.RunID != 0:
		res, err = s.DB.Exec(`UPDATE notifications SET read_at = ? WHERE read_at IS NULL AND run_id = ?`, now, f.RunID)
	case len(f.IDs) > 0:
		args := []any{now}
		for _, id := range f.IDs {
			args = append(args, id)
		}
		res, err = s.DB.Exec(`UPDATE notifications SET read_at = ? WHERE read_at IS NULL AND id IN (?`+strings.Repeat(",?", len(f.IDs)-1)+`)`, args...)
	default:
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// PruneNotifications deletes notifications recorded before cutoff.
func (s *Store) PruneNotifications(cutoff time.Time) (int, error) {
	res, err := s.DB.Exec(`DELETE FROM notifications WHERE at < ?`, cutoff.UTC())
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// IssuesForRun lists issues (open or resolved) whose context names run id,
// most recently seen first.
func (s *Store) IssuesForRun(id int64) ([]Issue, error) {
	rows, err := s.DB.Query(`SELECT `+issueCols+` FROM issues WHERE json_valid(context_json) AND json_extract(context_json, '$.run_id') = ?
		ORDER BY last_seen DESC, id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Issue{}
	for rows.Next() {
		i, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// PruneRunKV deletes the keys "<prefix><run id>" whose run no longer exists
// (per-run settings outliving the run's retention).
func (s *Store) PruneRunKV(prefix string) (int, error) {
	res, err := s.DB.Exec(`DELETE FROM kv WHERE substr(key, 1, ?) = ? AND CAST(substr(key, ?) AS INTEGER) NOT IN (SELECT id FROM runs)`,
		len(prefix), prefix, len(prefix)+1)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
