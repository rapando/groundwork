package store

import (
	"strings"
	"time"
)

// DiagRow mirrors a row of the diagnostics table.
type DiagRow struct {
	ID          int64
	Unit, Tool  string
	Severity    string
	Code        string
	Message     string
	Detail      string
	File        string
	Line, Col   int
	EndLine     int
	EndCol      int
	Link        string
	FixJSON     string
	ContentHash string
	SeenAt      time.Time
}

// ReplaceDiagnostics atomically swaps all rows for (unit, tool).
func (s *Store) ReplaceDiagnostics(unit, tool, hash string, rows []DiagRow) ([]int64, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM diagnostics WHERE unit = ? AND tool = ?`, unit, tool); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		var fix any
		if r.FixJSON != "" {
			fix = r.FixJSON
		}
		res, err := tx.Exec(`INSERT INTO diagnostics
			(unit, tool, severity, code, file, line, col, message, fix_json, content_hash, seen_at, detail, end_line, end_col, link)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			unit, tool, r.Severity, r.Code, r.File, r.Line, r.Col, r.Message, fix, hash, now, r.Detail, r.EndLine, r.EndCol, r.Link)
		if err != nil {
			return nil, err
		}
		id, _ := res.LastInsertId()
		ids = append(ids, id)
	}
	return ids, tx.Commit()
}

// DeleteUnitsExcept drops diagnostics of units that no longer exist.
func (s *Store) DeleteUnitsExcept(keep []string) error {
	if len(keep) == 0 {
		_, err := s.DB.Exec(`DELETE FROM diagnostics`)
		return err
	}
	ph := strings.TrimSuffix(strings.Repeat("?,", len(keep)), ",")
	args := make([]any, len(keep))
	for i, k := range keep {
		args[i] = k
	}
	_, err := s.DB.Exec(`DELETE FROM diagnostics WHERE unit NOT IN (`+ph+`)`, args...)
	return err
}

// ListDiagnostics returns rows filtered by any non-empty argument, ordered by
// file then position.
func (s *Store) ListDiagnostics(unit, severity, file string) ([]DiagRow, error) {
	q := `SELECT id, unit, tool, severity, code, file, line, col, message, COALESCE(fix_json,''), content_hash, seen_at, detail, end_line, end_col, link
		FROM diagnostics WHERE 1=1`
	var args []any
	for _, f := range []struct{ col, val string }{{"unit", unit}, {"severity", severity}, {"file", file}} {
		if f.val != "" {
			q += " AND " + f.col + " = ?"
			args = append(args, f.val)
		}
	}
	q += ` ORDER BY file, line, col, id`
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DiagRow
	for rows.Next() {
		var r DiagRow
		if err := rows.Scan(&r.ID, &r.Unit, &r.Tool, &r.Severity, &r.Code, &r.File, &r.Line, &r.Col, &r.Message,
			&r.FixJSON, &r.ContentHash, &r.SeenAt, &r.Detail, &r.EndLine, &r.EndCol, &r.Link); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
