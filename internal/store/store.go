package store

import (
	"context"
	"encoding/json"
	"errors"
	"fftagger/internal/model"
	"fftagger/internal/syncutil"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
)

type Store struct {
	mu     syncutil.Mutex
	db     *connection
	readMu sync.Mutex
	reader *connection
	notice atomic.Value
}

var ErrNotFound = errors.New("трек не найден")

func Open(path string) (*Store, error) {
	db, err := openSQLite(path)
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	s.notice.Store("")
	statements := []string{
		`PRAGMA journal_mode=WAL`, `PRAGMA synchronous=FULL`, `PRAGMA foreign_keys=ON`,
		`CREATE TABLE IF NOT EXISTS tracks (id INTEGER PRIMARY KEY, path TEXT NOT NULL UNIQUE, title TEXT NOT NULL, artist TEXT NOT NULL, album TEXT NOT NULL, title_key TEXT NOT NULL, artist_key TEXT NOT NULL, album_key TEXT NOT NULL, document TEXT NOT NULL, cover BLOB NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS idx_tracks_artist ON tracks(artist COLLATE NOCASE)`,
		`CREATE INDEX IF NOT EXISTS idx_tracks_album ON tracks(album COLLATE NOCASE)`,
		`CREATE INDEX IF NOT EXISTS idx_tracks_artist_key ON tracks(artist_key,title_key,id)`,
		`CREATE INDEX IF NOT EXISTS idx_tracks_album_key ON tracks(album_key,title_key,id)`,
		`CREATE INDEX IF NOT EXISTS idx_tracks_title_key ON tracks(title_key,id)`,
		`CREATE INDEX IF NOT EXISTS idx_tracks_without_cover ON tracks(artist_key,title_key,id) WHERE length(cover)=0`,
		`CREATE TABLE IF NOT EXISTS skipped_files (path TEXT PRIMARY KEY, root TEXT NOT NULL, path_key TEXT NOT NULL, kind TEXT NOT NULL, reason TEXT NOT NULL, detail TEXT NOT NULL, scanned_at TEXT NOT NULL)`,
		`CREATE INDEX IF NOT EXISTS idx_skipped_root ON skipped_files(root)`,
		`CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS sources (id INTEGER PRIMARY KEY AUTOINCREMENT, path TEXT NOT NULL UNIQUE, added_at TEXT NOT NULL, last_scan_at TEXT NOT NULL DEFAULT '')`,
	}
	for _, q := range statements {
		if _, err = db.query(q); err != nil {
			db.close()
			return nil, err
		}
	}
	if err = s.migrateAudioStorage(); err != nil {
		db.close()
		return nil, err
	}
	s.reader, err = openSQLite(path)
	if err != nil {
		db.close()
		return nil, err
	}
	if _, err = s.reader.query(`PRAGMA query_only=ON`); err != nil {
		s.reader.close()
		db.close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readMu.Lock()
	defer s.readMu.Unlock()
	s.reader.close()
	s.db.close()
}
func decode(r row) (model.Track, error) {
	var t model.Track
	err := json.Unmarshal([]byte(r[1].(string)), &t)
	t.ID = r[0].(int64)
	return t, err
}
func (s *Store) Get(id int64) (model.Track, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	r, err := s.reader.query(`SELECT id,document FROM tracks WHERE id=?`, id)
	if err != nil {
		return model.Track{}, err
	}
	if len(r) == 0 {
		return model.Track{}, ErrNotFound
	}
	return decode(r[0])
}
func (s *Store) ByPath(path string) (model.Track, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	r, err := s.reader.query(`SELECT id,document FROM tracks WHERE path=?`, path)
	if err != nil {
		return model.Track{}, err
	}
	if len(r) == 0 {
		return model.Track{}, ErrNotFound
	}
	return decode(r[0])
}
func (s *Store) Cover(id int64) ([]byte, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	r, e := s.reader.query(`SELECT cover FROM tracks WHERE id=?`, id)
	if e != nil {
		return nil, e
	}
	if len(r) == 0 {
		return nil, ErrNotFound
	}
	return r[0][0].([]byte), nil
}

// Store metadata and artwork only. The recording stays at its original path;
// stat checks guard against changes after probing, without reading audio bytes.
func (s *Store) Put(ctx context.Context, t *model.Track, source string) (err error) {
	before, err := os.Stat(source)
	if err != nil {
		return err
	}
	if before.Size() != t.Size || before.ModTime().UnixNano() != t.ModTime {
		return errors.New("файл изменился во время чтения; повторите сканирование")
	}
	doc, err := json.Marshal(t)
	if err != nil {
		return err
	}
	if err = s.mu.LockContext(ctx); err != nil {
		return err
	}
	defer s.mu.Unlock()
	if _, err = s.db.query(`BEGIN IMMEDIATE`); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			s.db.query(`ROLLBACK`)
		}
	}()
	// Keep IDs above the previous library's high-water mark after clearing it,
	// so stale editor requests cannot address a newly imported, different track.
	_, err = s.db.query(`INSERT INTO tracks(id,path,title,artist,album,title_key,artist_key,album_key,document,cover) VALUES((SELECT max(coalesce((SELECT max(id) FROM tracks),0),coalesce((SELECT CAST(value AS INTEGER) FROM settings WHERE key='track_id_floor'),0))+1),?,?,?,?,?,?,?,?,?) ON CONFLICT(path) DO UPDATE SET title=excluded.title,artist=excluded.artist,album=excluded.album,title_key=excluded.title_key,artist_key=excluded.artist_key,album_key=excluded.album_key,document=excluded.document,cover=excluded.cover`, t.Path, t.Tags.Title, t.Tags.Artist, t.Tags.Album, strings.ToLower(t.Tags.Title), strings.ToLower(t.Tags.Artist), strings.ToLower(t.Tags.Album), string(doc), t.Cover)
	if err != nil {
		return err
	}
	r, err := s.db.query(`SELECT id FROM tracks WHERE path=?`, t.Path)
	if err != nil {
		return err
	}
	t.ID = r[0][0].(int64)
	after, err := os.Stat(source)
	if err != nil {
		return err
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) || !os.SameFile(before, after) {
		return errors.New("файл изменился во время импорта")
	}
	// Resolve stale reports as part of this commit, avoiding a second write
	// transaction for every imported track.
	if _, err = s.db.query(`DELETE FROM skipped_files WHERE path=?`, t.Path); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	_, err = s.db.query(`COMMIT`)
	return err
}

type Query struct {
	Search, Artist, Album, Sort, Order string
	Cover                              string
	Offset, Limit                      int
}
type Page struct {
	Tracks []model.Track `json:"tracks"`
	Total  int64         `json:"total"`
}

func (s *Store) List(q Query) (Page, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()
	where := ` WHERE 1=1`
	args := []any{}
	switch q.Cover {
	case "missing":
		where += ` AND length(cover)=0`
	case "present":
		where += ` AND length(cover)>0`
	}
	if q.Search != "" {
		where += ` AND (instr(title_key,?)>0 OR instr(artist_key,?)>0 OR instr(album_key,?)>0 OR instr(lower(path),?)>0)`
		v := strings.ToLower(q.Search)
		args = append(args, v, v, v, v)
	}
	for _, f := range []struct{ column, value string }{{"artist_key", q.Artist}, {"album_key", q.Album}} {
		if f.value != "" {
			v := strings.ToLower(f.value)
			where += ` AND ` + f.column + `>=? AND ` + f.column + `<?`
			args = append(args, v, v+"\U0010ffff")
		}
	}
	column := map[string]string{"title": "title_key", "artist": "artist_key", "album": "album_key", "added": "id"}[q.Sort]
	if column == "" {
		column = "artist_key"
	}
	direction := " ASC"
	if q.Order == "desc" {
		direction = " DESC"
	}
	if q.Limit <= 0 || q.Limit > 200 {
		q.Limit = 100
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	// Keep count and page on the same WAL snapshot while the scanner imports.
	if _, err := s.reader.query(`BEGIN`); err != nil {
		return Page{}, err
	}
	defer s.reader.query(`ROLLBACK`)
	count, err := s.reader.query(`SELECT COUNT(*) FROM tracks`+where, args...)
	if err != nil {
		return Page{}, err
	}
	r, err := s.reader.query(`SELECT id,document FROM tracks`+where+` ORDER BY `+column+direction+`,title_key`+direction+`,id`+direction+` LIMIT ? OFFSET ?`, append(args, q.Limit, q.Offset)...)
	if err != nil {
		return Page{}, err
	}
	p := Page{Tracks: []model.Track{}, Total: count[0][0].(int64)}
	for _, v := range r {
		t, e := decode(v)
		if e != nil {
			return p, fmt.Errorf("read track: %w", e)
		}
		p.Tracks = append(p.Tracks, t)
	}
	return p, nil
}
