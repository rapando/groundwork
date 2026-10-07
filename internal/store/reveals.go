package store

import "time"

// Reveal is one audit row: a secret was shown in the UI. It holds no value.
type Reveal struct {
	ID         int64     `json:"id"`
	Kind       string    `json:"kind"`
	Name       string    `json:"name"`
	File       string    `json:"file"`
	RevealedAt time.Time `json:"revealed_at"`
}

func (s *Store) RecordReveal(kind, name, file string) error {
	_, err := s.DB.Exec(`INSERT INTO secret_reveals(kind, name, file, revealed_at) VALUES(?,?,?,?)`, kind, name, file, time.Now().UTC())
	return err
}

func (s *Store) ListReveals(limit int) ([]Reveal, error) {
	rows, err := s.DB.Query(`SELECT id, kind, name, file, revealed_at FROM secret_reveals ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Reveal{}
	for rows.Next() {
		var r Reveal
		if err := rows.Scan(&r.ID, &r.Kind, &r.Name, &r.File, &r.RevealedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
