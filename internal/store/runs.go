package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Run statuses.
const (
	StatusQueued          = "queued"
	StatusRunning         = "running"
	StatusWaitingApproval = "waiting_approval"
	StatusSucceeded       = "succeeded"
	StatusFailed          = "failed"
	StatusCancelled       = "cancelled"
)

type Run struct {
	ID        int64           `json:"id"`
	Kind      string          `json:"kind"`
	Target    json.RawMessage `json:"target"`
	Argv      json.RawMessage `json:"argv"`
	Status    string          `json:"status"`
	Stage     string          `json:"stage"`
	Commit    string          `json:"commit,omitempty"`
	CreatedAt time.Time       `json:"created_at"`
	StartedAt *time.Time      `json:"started_at,omitempty"`
	EndedAt   *time.Time      `json:"ended_at,omitempty"`
	ExitCode  *int            `json:"exit_code,omitempty"`
	Summary   json.RawMessage `json:"summary"`
}

type RunStage struct {
	RunID     int64      `json:"run_id"`
	Name      string     `json:"name"`
	Status    string     `json:"status"` // pending | running | succeeded | failed | skipped | cancelled
	StartedAt *time.Time `json:"started_at,omitempty"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
	Detail    string     `json:"detail,omitempty"`
}

type Approval struct {
	RunID       int64     `json:"run_id"`
	PlanSHA256  string    `json:"plan_sha256"`
	StateSerial int64     `json:"state_serial"`
	ApprovedBy  string    `json:"approved_by"`
	ApprovedAt  time.Time `json:"approved_at"`
	ConfirmText string    `json:"confirm_text"`
	AckDanger   bool      `json:"acknowledged_danger"`
}

var ErrNoRun = errors.New("run not found")

func (s *Store) CreateRun(kind string, target, argv any, commit string) (*Run, error) {
	t, _ := json.Marshal(target)
	a, _ := json.Marshal(argv)
	now := time.Now().UTC()
	res, err := s.DB.Exec(`INSERT INTO runs(kind, target_json, argv_json, status, stage, "commit", created_at)
		VALUES(?,?,?,?,?,?,?)`, kind, string(t), string(a), StatusQueued, "", commit, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.GetRun(id)
}

const runCols = `id, kind, target_json, argv_json, status, stage, "commit", created_at, started_at, ended_at, exit_code, summary_json`

func scanRun(sc interface{ Scan(...any) error }) (*Run, error) {
	var r Run
	var target, argv, summary string
	var started, ended sql.NullTime
	var exit sql.NullInt64
	var created sql.NullTime
	if err := sc.Scan(&r.ID, &r.Kind, &target, &argv, &r.Status, &r.Stage, &r.Commit, &created, &started, &ended, &exit, &summary); err != nil {
		return nil, err
	}
	r.Target, r.Argv, r.Summary = json.RawMessage(target), json.RawMessage(argv), json.RawMessage(summary)
	r.CreatedAt = created.Time
	if started.Valid {
		r.StartedAt = &started.Time
	}
	if ended.Valid {
		r.EndedAt = &ended.Time
	}
	if exit.Valid {
		c := int(exit.Int64)
		r.ExitCode = &c
	}
	return &r, nil
}

func (s *Store) GetRun(id int64) (*Run, error) {
	r, err := scanRun(s.DB.QueryRow(`SELECT `+runCols+` FROM runs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoRun
	}
	return r, err
}

// RunUpdate changes selected fields; nil/zero means "leave as is".
type RunUpdate struct {
	Status   string
	Stage    *string
	Started  *time.Time
	Ended    *time.Time
	ExitCode *int
	Summary  any
	Argv     any
}

func (s *Store) UpdateRun(id int64, u RunUpdate) error {
	var sets []string
	var args []any
	add := func(col string, v any) { sets = append(sets, col+" = ?"); args = append(args, v) }
	if u.Status != "" {
		add("status", u.Status)
	}
	if u.Stage != nil {
		add("stage", *u.Stage)
	}
	if u.Started != nil {
		add("started_at", u.Started.UTC())
	}
	if u.Ended != nil {
		add("ended_at", u.Ended.UTC())
	}
	if u.ExitCode != nil {
		add("exit_code", *u.ExitCode)
	}
	if u.Summary != nil {
		b, _ := json.Marshal(u.Summary)
		add("summary_json", string(b))
	}
	if u.Argv != nil {
		b, _ := json.Marshal(u.Argv)
		add("argv_json", string(b))
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, id)
	_, err := s.DB.Exec(`UPDATE runs SET `+strings.Join(sets, ", ")+` WHERE id = ?`, args...)
	return err
}

type RunFilter struct {
	Kind   string
	Status string
	Before int64 // cursor: ids below this
	Limit  int
}

func (s *Store) ListRuns(f RunFilter) ([]*Run, error) {
	q := `SELECT ` + runCols + ` FROM runs WHERE 1=1`
	var args []any
	if f.Kind != "" {
		q += ` AND kind = ?`
		args = append(args, f.Kind)
	}
	if f.Status != "" {
		q += ` AND status = ?`
		args = append(args, f.Status)
	}
	if f.Before > 0 {
		q += ` AND id < ?`
		args = append(args, f.Before)
	}
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 50
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

func (s *Store) UpsertStage(st RunStage) error {
	st.StartedAt, st.EndedAt = utcPtr(st.StartedAt), utcPtr(st.EndedAt)
	_, err := s.DB.Exec(`INSERT INTO run_stages(run_id, name, status, started_at, ended_at, detail) VALUES(?,?,?,?,?,?)
		ON CONFLICT(run_id, name) DO UPDATE SET status=excluded.status,
		started_at=COALESCE(excluded.started_at, run_stages.started_at),
		ended_at=excluded.ended_at, detail=excluded.detail`,
		st.RunID, st.Name, st.Status, st.StartedAt, st.EndedAt, st.Detail)
	return err
}

// ListStages returns stages in the order they were first created.
func (s *Store) ListStages(runID int64) ([]RunStage, error) {
	rows, err := s.DB.Query(`SELECT run_id, name, status, started_at, ended_at, detail FROM run_stages WHERE run_id = ? ORDER BY rowid`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RunStage
	for rows.Next() {
		var st RunStage
		var a, b sql.NullTime
		if err := rows.Scan(&st.RunID, &st.Name, &st.Status, &a, &b, &st.Detail); err != nil {
			return nil, err
		}
		if a.Valid {
			st.StartedAt = &a.Time
		}
		if b.Valid {
			st.EndedAt = &b.Time
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s *Store) SaveApproval(a Approval) error {
	_, err := s.DB.Exec(`INSERT INTO approvals(run_id, plan_sha256, state_serial, approved_by, approved_at, confirm_text, acknowledged_danger)
		VALUES(?,?,?,?,?,?,?)`, a.RunID, a.PlanSHA256, a.StateSerial, a.ApprovedBy, a.ApprovedAt.UTC(), a.ConfirmText, a.AckDanger)
	return err
}

func (s *Store) GetApproval(runID int64) (*Approval, error) {
	var a Approval
	err := s.DB.QueryRow(`SELECT run_id, plan_sha256, state_serial, approved_by, approved_at, confirm_text, acknowledged_danger FROM approvals WHERE run_id = ?`, runID).
		Scan(&a.RunID, &a.PlanSHA256, &a.StateSerial, &a.ApprovedBy, &a.ApprovedAt, &a.ConfirmText, &a.AckDanger)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &a, err
}

// InterruptedRuns marks every run that was queued or running when the process
// died as failed ("interrupted") and returns them, so callers can raise issues.
// Runs waiting for approval survive: their plan file is still on disk.
func (s *Store) InterruptedRuns() ([]*Run, error) {
	rows, err := s.DB.Query(`SELECT ` + runCols + ` FROM runs WHERE status IN ('queued','running')`)
	if err != nil {
		return nil, err
	}
	var runs []*Run
	for rows.Next() {
		r, err := scanRun(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		runs = append(runs, r)
	}
	rows.Close()
	now := time.Now().UTC()
	for _, r := range runs {
		if _, err := s.DB.Exec(`UPDATE runs SET status = 'failed', ended_at = ?, summary_json = ? WHERE id = ?`,
			now, `{"error":"interrupted: groundwork stopped while this run was in progress"}`, r.ID); err != nil {
			return nil, err
		}
		if _, err := s.DB.Exec(`UPDATE run_stages SET status = 'failed', ended_at = ?, detail = 'interrupted' WHERE run_id = ? AND status = 'running'`, now, r.ID); err != nil {
			return nil, err
		}
		r.Status = StatusFailed
	}
	return runs, nil
}

// PruneRuns deletes runs beyond the newest keep or older than maxAge and
// returns their ids (callers remove the artefact directories).
func (s *Store) PruneRuns(keep int, maxAge time.Duration) ([]int64, error) {
	cutoff := time.Now().Add(-maxAge).UTC()
	rows, err := s.DB.Query(`SELECT id FROM runs WHERE status NOT IN ('queued','running','waiting_approval')
		AND (id NOT IN (SELECT id FROM runs ORDER BY id DESC LIMIT ?) OR created_at < ?)`, keep, cutoff)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if _, err := s.DB.Exec(`DELETE FROM runs WHERE id = ?`, id); err != nil {
			return nil, err
		}
	}
	return ids, nil
}

// RaiseIssue inserts an open issue unless an identical open one exists.
func (s *Store) RaiseIssue(ruleID, target string, ctx any) error {
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM issues WHERE rule_id = ? AND target = ? AND status = 'open'`, ruleID, target).Scan(&n); err != nil {
		return err
	}
	now := time.Now().UTC()
	if n > 0 {
		_, err := s.DB.Exec(`UPDATE issues SET last_seen = ? WHERE rule_id = ? AND target = ? AND status = 'open'`, now, ruleID, target)
		return err
	}
	b, _ := json.Marshal(ctx)
	_, err := s.DB.Exec(`INSERT INTO issues(rule_id, target, status, first_seen, last_seen, context_json) VALUES(?,?,?,?,?,?)`,
		ruleID, target, "open", now, now, string(b))
	return err
}
