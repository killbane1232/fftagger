package store

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Settings struct {
	Theme       string `json:"theme"`
	ScanWorkers int    `json:"scan_workers"`
}

type SettingsPatch struct {
	Theme       *string `json:"theme"`
	ScanWorkers *int    `json:"scan_workers"`
}

func (s *Store) Settings() (Settings, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	return readSettings(s.reader)
}
func readSettings(c *connection) (Settings, error) {
	r, err := c.query(`SELECT key,value FROM settings WHERE key IN ('theme','scan_workers')`)
	if err != nil {
		return Settings{}, err
	}
	v := Settings{Theme: "system"}
	for _, row := range r {
		switch row[0].(string) {
		case "theme":
			v.Theme = row[1].(string)
		case "scan_workers":
			v.ScanWorkers, err = strconv.Atoi(row[1].(string))
			if err != nil {
				return Settings{}, errors.New("не удалось прочитать настройку потоков")
			}
		}
	}
	return v, nil
}
func (s *Store) SetSettings(v Settings) error {
	_, err := s.UpdateSettings(SettingsPatch{Theme: &v.Theme, ScanWorkers: &v.ScanWorkers})
	return err
}

// Apply only fields supplied by the caller. Theme updates must not reset scan
// settings, and concurrent requests must not overwrite each other's fields.
func (s *Store) UpdateSettings(p SettingsPatch) (v Settings, err error) {
	if p.Theme != nil && *p.Theme != "system" && *p.Theme != "light" && *p.Theme != "dark" {
		return v, errors.New("неизвестная тема")
	}
	if p.ScanWorkers != nil && (*p.ScanWorkers < 0 || *p.ScanWorkers > 16) {
		return v, errors.New("число потоков должно быть от 1 до 16; 0 — автоматически")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err = s.db.query(`BEGIN IMMEDIATE`); err != nil {
		return v, err
	}
	defer func() {
		if err != nil {
			s.db.query(`ROLLBACK`)
		}
	}()
	if p.Theme != nil {
		_, err = s.db.query(`INSERT INTO settings(key,value) VALUES('theme',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, *p.Theme)
		if err != nil {
			return v, err
		}
	}
	if p.ScanWorkers != nil {
		_, err = s.db.query(`INSERT INTO settings(key,value) VALUES('scan_workers',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, strconv.Itoa(*p.ScanWorkers))
		if err != nil {
			return v, err
		}
	}
	if v, err = readSettings(s.db); err != nil {
		return v, err
	}
	_, err = s.db.query(`COMMIT`)
	return v, err
}

type Counts struct {
	Tracks       int64 `json:"tracks"`
	WithoutCover int64 `json:"without_cover"`
	Skipped      int64 `json:"skipped"`
}

func (s *Store) Counts() (Counts, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	r, err := s.reader.query(`SELECT (SELECT count(*) FROM tracks),(SELECT count(*) FROM tracks WHERE length(cover)=0),(SELECT count(*) FROM skipped_files)`)
	if err != nil {
		return Counts{}, err
	}
	return Counts{r[0][0].(int64), r[0][1].(int64), r[0][2].(int64)}, nil
}

type SkippedFile struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Root      string `json:"root"`
	Kind      string `json:"kind"`
	Reason    string `json:"reason"`
	Detail    string `json:"detail"`
	ScannedAt string `json:"scanned_at"`
}
type SkippedPage struct {
	Files []SkippedFile `json:"files"`
	Total int64         `json:"total"`
}

// Each selected folder has its most recent scan report. A cancelled scan keeps
// the partial report; reports for other folders are left intact.
func (s *Store) ClearSkipped(root string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.query(`DELETE FROM skipped_files WHERE root=?`, root)
	return err
}
func (s *Store) RecordSkipped(root, path, kind, reason, detail string) error {
	return s.RecordSkippedContext(context.Background(), root, path, kind, reason, detail)
}
func (s *Store) RecordSkippedContext(ctx context.Context, root, path, kind, reason, detail string) error {
	if err := s.mu.LockContext(ctx); err != nil {
		return err
	}
	defer s.mu.Unlock()
	_, err := s.db.query(`INSERT INTO skipped_files(path,root,path_key,kind,reason,detail,scanned_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(path) DO UPDATE SET root=excluded.root,kind=excluded.kind,reason=excluded.reason,detail=excluded.detail,scanned_at=excluded.scanned_at`, path, root, strings.ToLower(path), kind, reason, detail, time.Now().UTC().Format(time.RFC3339))
	return err
}
func (s *Store) ResolveSkipped(path string) error {
	return s.ResolveSkippedContext(context.Background(), path)
}
func (s *Store) ResolveSkippedContext(ctx context.Context, path string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Most unchanged tracks have no report. Keep this fast path on the WAL
	// reader rather than waiting for an unrelated large audio import.
	s.readMu.Lock()
	rows, err := s.reader.query(`SELECT 1 FROM skipped_files WHERE path=?`, path)
	s.readMu.Unlock()
	if err != nil || len(rows) == 0 {
		return err
	}
	if err = s.mu.LockContext(ctx); err != nil {
		return err
	}
	defer s.mu.Unlock()
	_, err = s.db.query(`DELETE FROM skipped_files WHERE path=?`, path)
	return err
}
func (s *Store) ListSkipped(search, kind string, offset int) (SkippedPage, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	if offset < 0 {
		offset = 0
	}
	where := ` WHERE 1=1`
	args := []any{}
	if search != "" {
		where += ` AND instr(path_key,?)>0`
		args = append(args, strings.ToLower(search))
	}
	if kind != "" {
		where += ` AND kind=?`
		args = append(args, kind)
	}
	if _, err := s.reader.query(`BEGIN`); err != nil {
		return SkippedPage{}, err
	}
	defer s.reader.query(`ROLLBACK`)
	count, err := s.reader.query(`SELECT count(*) FROM skipped_files`+where, args...)
	if err != nil {
		return SkippedPage{}, err
	}
	rows, err := s.reader.query(`SELECT path,root,kind,reason,detail,scanned_at FROM skipped_files`+where+` ORDER BY path_key,path LIMIT 100 OFFSET ?`, append(args, offset)...)
	if err != nil {
		return SkippedPage{}, err
	}
	p := SkippedPage{Files: []SkippedFile{}, Total: count[0][0].(int64)}
	for _, r := range rows {
		path := r[0].(string)
		p.Files = append(p.Files, SkippedFile{Path: path, Name: filepath.Base(path), Root: r[1].(string), Kind: r[2].(string), Reason: r[3].(string), Detail: r[4].(string), ScannedAt: r[5].(string)})
	}
	return p, nil
}
