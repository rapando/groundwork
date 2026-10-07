package store

import (
	"database/sql"
	"errors"
	"time"
)

type HostStatus struct {
	Host      string    `json:"host"`
	Reachable bool      `json:"reachable"`
	LatencyMS int       `json:"latency_ms"`
	Msg       string    `json:"msg,omitempty"`
	CheckedAt time.Time `json:"checked_at"`
}

func (s *Store) SetHostStatus(scope string, h HostStatus) error {
	_, err := s.DB.Exec(`INSERT INTO host_status(host, scope, reachable, latency_ms, msg, checked_at) VALUES(?,?,?,?,?,?)
		ON CONFLICT(host, scope) DO UPDATE SET reachable=excluded.reachable, latency_ms=excluded.latency_ms, msg=excluded.msg, checked_at=excluded.checked_at`,
		h.Host, scope, h.Reachable, h.LatencyMS, h.Msg, h.CheckedAt.UTC())
	return err
}

func (s *Store) HostStatuses(scope string) (map[string]HostStatus, error) {
	rows, err := s.DB.Query(`SELECT host, reachable, latency_ms, msg, checked_at FROM host_status WHERE scope = ?`, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]HostStatus{}
	for rows.Next() {
		var h HostStatus
		if err := rows.Scan(&h.Host, &h.Reachable, &h.LatencyMS, &h.Msg, &h.CheckedAt); err != nil {
			return nil, err
		}
		out[h.Host] = h
	}
	return out, rows.Err()
}

type Facts struct {
	JSON       string    `json:"-"`
	GatheredAt time.Time `json:"gathered_at"`
}

// SetFacts stores a host's (already filtered) facts for a scope.
func (s *Store) SetFacts(scope, host, js string) error {
	_, err := s.DB.Exec(`INSERT INTO facts(host, env, json, gathered_at) VALUES(?,?,?,?)
		ON CONFLICT(host, env) DO UPDATE SET json=excluded.json, gathered_at=excluded.gathered_at`, host, scope, js, time.Now().UTC())
	return err
}

func (s *Store) GetFacts(scope, host string) (*Facts, error) {
	var f Facts
	err := s.DB.QueryRow(`SELECT json, gathered_at FROM facts WHERE host = ? AND env = ?`, host, scope).Scan(&f.JSON, &f.GatheredAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &f, err
}

func (s *Store) AllFacts(scope string) (map[string]Facts, error) {
	rows, err := s.DB.Query(`SELECT host, json, gathered_at FROM facts WHERE env = ?`, scope)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Facts{}
	for rows.Next() {
		var h string
		var f Facts
		if err := rows.Scan(&h, &f.JSON, &f.GatheredAt); err != nil {
			return nil, err
		}
		out[h] = f
	}
	return out, rows.Err()
}
