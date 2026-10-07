package store

import (
	"database/sql"
	"errors"
	"time"
)

// DriftRow is one drifted attribute (or, with an empty AttrPath, a resource
// deleted outside Terraform). Values are already masked.
type DriftRow struct {
	ID         int64     `json:"id"`
	Root       string    `json:"root"`
	Env        string    `json:"env"`
	Address    string    `json:"address"`
	Action     string    `json:"action"` // update | delete
	AttrPath   string    `json:"attr_path"`
	Code       string    `json:"code"`   // what state/code says
	Actual     string    `json:"actual"` // what exists now
	RunID      int64     `json:"run_id"`
	DetectedAt time.Time `json:"detected_at"`
}

// ReplaceDrift swaps all drift rows of a root/env for a new detection result.
func (s *Store) ReplaceDrift(root, env string, runID int64, rows []DriftRow) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM drift WHERE root = ? AND env = ?`, root, env); err != nil {
		return err
	}
	now := time.Now().UTC()
	for _, r := range rows {
		if _, err := tx.Exec(`INSERT INTO drift(root, env, address, attr_path, code_value, actual_value, detected_at, action, run_id)
			VALUES(?,?,?,?,?,?,?,?,?)`, root, env, r.Address, r.AttrPath, r.Code, r.Actual, now, r.Action, runID); err != nil {
			return err
		}
	}
	return tx.Commit()
}

const driftCols = `id, root, env, address, action, attr_path, COALESCE(code_value,''), COALESCE(actual_value,''), run_id, detected_at`

func scanDrift(sc interface{ Scan(...any) error }) (DriftRow, error) {
	var r DriftRow
	err := sc.Scan(&r.ID, &r.Root, &r.Env, &r.Address, &r.Action, &r.AttrPath, &r.Code, &r.Actual, &r.RunID, &r.DetectedAt)
	return r, err
}

// ListDrift returns drift rows, optionally filtered by root and env.
func (s *Store) ListDrift(root, env string) ([]DriftRow, error) {
	q := `SELECT ` + driftCols + ` FROM drift WHERE 1=1`
	var args []any
	if root != "" {
		q += ` AND root = ?`
		args = append(args, root)
	}
	if env != "" {
		q += ` AND env = ?`
		args = append(args, env)
	}
	rows, err := s.DB.Query(q+` ORDER BY address, attr_path`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DriftRow{}
	for rows.Next() {
		r, err := scanDrift(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

var ErrNoDrift = errors.New("drift record not found")

func (s *Store) GetDrift(id int64) (*DriftRow, error) {
	r, err := scanDrift(s.DB.QueryRow(`SELECT `+driftCols+` FROM drift WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNoDrift
	}
	return &r, err
}
