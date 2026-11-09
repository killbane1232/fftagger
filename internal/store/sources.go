package store

import (
	"errors"
	"path/filepath"
	"time"
)

var ErrSourceNotFound = errors.New("источник не найден")

type Source struct {
	ID         int64  `json:"id"`
	Path       string `json:"path"`
	Name       string `json:"name"`
	AddedAt    string `json:"added_at"`
	LastScanAt string `json:"last_scan_at"`
}

func decodeSource(r row) Source {
	path := r[1].(string)
	return Source{ID: r[0].(int64), Path: path, Name: filepath.Base(path), AddedAt: r[2].(string), LastScanAt: r[3].(string)}
}
func (s *Store) Sources() ([]Source, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	rows, err := s.reader.query(`SELECT id,path,added_at,last_scan_at FROM sources ORDER BY id`)
	if err != nil {
		return nil, err
	}
	result := make([]Source, 0, len(rows))
	for _, r := range rows {
		result = append(result, decodeSource(r))
	}
	return result, nil
}

// The library supplies a canonical absolute path. Re-adding a source preserves
// its ID and position, including when a symlink alias was selected.
func (s *Store) RememberSource(path string, scanned bool) (Source, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	last := ""
	if scanned {
		last = now
	}
	_, err := s.db.query(`INSERT INTO sources(path,added_at,last_scan_at) VALUES(?,?,?) ON CONFLICT(path) DO UPDATE SET last_scan_at=CASE WHEN excluded.last_scan_at='' THEN sources.last_scan_at ELSE excluded.last_scan_at END`, path, now, last)
	if err != nil {
		return Source{}, err
	}
	rows, err := s.db.query(`SELECT id,path,added_at,last_scan_at FROM sources WHERE path=?`, path)
	if err != nil {
		return Source{}, err
	}
	return decodeSource(rows[0]), nil
}
func (s *Store) Source(id int64) (Source, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	rows, err := s.reader.query(`SELECT id,path,added_at,last_scan_at FROM sources WHERE id=?`, id)
	if err != nil {
		return Source{}, err
	}
	if len(rows) == 0 {
		return Source{}, ErrSourceNotFound
	}
	return decodeSource(rows[0]), nil
}
func (s *Store) RemoveSource(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.query(`DELETE FROM sources WHERE id=?`, id)
	return err
}
