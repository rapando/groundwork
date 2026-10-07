package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

const (
	IssueOpen     = "open"
	IssueResolved = "resolved"
)

// Issue is a matched rule on a target. (RuleID, Target, Key) identifies it, so
// the same problem recurring updates one row instead of piling up.
type Issue struct {
	ID         int64           `json:"id"`
	RuleID     string          `json:"rule_id"`
	Target     string          `json:"target"`
	Key        string          `json:"key,omitempty"`
	Source     string          `json:"source"` // run | diagnostic
	Status     string          `json:"status"`
	FirstSeen  time.Time       `json:"first_seen"`
	LastSeen   time.Time       `json:"last_seen"`
	ResolvedAt *time.Time      `json:"resolved_at,omitempty"`
	Resolution string          `json:"resolution,omitempty"`
	Context    json.RawMessage `json:"context"`
}

// UpsertIssue records an occurrence seen at `seen` (when the evidence was
// produced, not when it was processed). A resolved issue re-opens only for
// evidence newer than its resolution, so re-reading an old log can't undo a fix.
func (s *Store) UpsertIssue(ruleID, target, key, source string, seen time.Time, ctx any) (id int64, changed bool, err error) {
	b, err := json.Marshal(ctx)
	if err != nil {
		return 0, false, err
	}
	seen = seen.UTC()
	var status string
	var lastSeen time.Time
	var resolved sql.NullTime
	err = s.DB.QueryRow(`SELECT id, status, last_seen, resolved_at FROM issues WHERE rule_id = ? AND target = ? AND key = ?`, ruleID, target, key).
		Scan(&id, &status, &lastSeen, &resolved)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, err := s.DB.Exec(`INSERT INTO issues(rule_id, target, key, source, status, first_seen, last_seen, context_json)
			VALUES(?,?,?,?,?,?,?,?)`, ruleID, target, key, source, IssueOpen, seen, seen, string(b))
		if err != nil {
			return 0, false, err
		}
		id, _ = res.LastInsertId()
		return id, true, nil
	case err != nil:
		return 0, false, err
	}
	if status == IssueResolved {
		if resolved.Valid && !seen.After(resolved.Time) {
			return id, false, nil
		}
		_, err = s.DB.Exec(`UPDATE issues SET status = ?, first_seen = ?, last_seen = ?, context_json = ?, resolved_at = NULL, resolution = '' WHERE id = ?`,
			IssueOpen, seen, seen, string(b), id)
		return id, true, err
	}
	if seen.Before(lastSeen) {
		return id, false, nil
	}
	_, err = s.DB.Exec(`UPDATE issues SET last_seen = ?, context_json = ? WHERE id = ?`, seen, string(b), id)
	return id, false, err
}

// ResolveIssue closes an open issue. at is when the fixing evidence was
// produced; evidence older than the latest occurrence doesn't resolve it.
func (s *Store) ResolveIssue(id int64, at time.Time, resolution string) (bool, error) {
	res, err := s.DB.Exec(`UPDATE issues SET status = ?, resolved_at = ?, resolution = ? WHERE id = ? AND status = ? AND last_seen <= ?`,
		IssueResolved, at.UTC(), resolution, id, IssueOpen, at.UTC())
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// OpenIssuesOn lists open issues for a target.
func (s *Store) OpenIssuesOn(target string) ([]Issue, error) {
	rows, err := s.DB.Query(`SELECT `+issueCols+` FROM issues WHERE target = ? AND status = ?`, target, IssueOpen)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Issue
	for rows.Next() {
		i, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// OpenIssuesBySource lists open issues from one source (run | diagnostic).
func (s *Store) OpenIssuesBySource(source string) ([]Issue, error) {
	rows, err := s.DB.Query(`SELECT `+issueCols+` FROM issues WHERE source = ? AND status = ?`, source, IssueOpen)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Issue
	for rows.Next() {
		i, err := scanIssue(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// CountResolvedSince counts issues resolved after t.
func (s *Store) CountResolvedSince(t time.Time) int {
	var n int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM issues WHERE status = ? AND resolved_at >= ?`, IssueResolved, t.UTC()).Scan(&n)
	return n
}

const issueCols = `id, rule_id, target, key, source, status, first_seen, last_seen, resolved_at, resolution, context_json`

func scanIssue(sc interface{ Scan(...any) error }) (Issue, error) {
	var i Issue
	var ctx string
	var resolved sql.NullTime
	err := sc.Scan(&i.ID, &i.RuleID, &i.Target, &i.Key, &i.Source, &i.Status, &i.FirstSeen, &i.LastSeen, &resolved, &i.Resolution, &ctx)
	if resolved.Valid {
		t := resolved.Time
		i.ResolvedAt = &t
	}
	i.Context = json.RawMessage(ctx)
	return i, err
}

func (s *Store) GetIssue(id int64) (*Issue, error) {
	i, err := scanIssue(s.DB.QueryRow(`SELECT `+issueCols+` FROM issues WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	return &i, nil
}

// ListIssues returns issues by status ("" = all), newest activity first.
func (s *Store) ListIssues(status string, since time.Time, limit int) ([]Issue, error) {
	q := `SELECT ` + issueCols + ` FROM issues WHERE 1=1`
	var args []any
	if status != "" {
		q += ` AND status = ?`
		args = append(args, status)
	}
	if !since.IsZero() {
		q += ` AND COALESCE(resolved_at, last_seen) >= ?`
		args = append(args, since.UTC())
	}
	q += ` ORDER BY COALESCE(resolved_at, last_seen) DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.DB.Query(q, args...)
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
