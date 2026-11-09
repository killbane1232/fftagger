package store

import (
	"context"
	"fmt"
)

func (s *Store) Notice() string { return s.notice.Load().(string) }

// Drop legacy audio BLOBs while keeping every catalog entry and cover. A pending
// marker allows compaction to retry after interruption or insufficient space.
// Called before opening the reader and before admitting application requests.
func (s *Store) migrateAudioStorage() (err error) {
	rows, err := s.db.query(`SELECT 1 FROM sqlite_master WHERE type='table' AND name='audio_chunks'`)
	if err != nil {
		return err
	}
	if len(rows) > 0 {
		if _, err = s.db.query(`BEGIN IMMEDIATE`); err != nil {
			return err
		}
		defer func() {
			if err != nil {
				s.db.query(`ROLLBACK`)
			}
		}()
		for _, sql := range []string{
			`DROP TABLE audio_chunks`,
			`INSERT OR REPLACE INTO settings(key,value) VALUES('compact_pending','1')`,
			`PRAGMA user_version=4`,
			`COMMIT`,
		} {
			if _, err = s.db.query(sql); err != nil {
				return err
			}
		}
	} else if _, err = s.db.query(`PRAGMA user_version=4`); err != nil {
		return err
	}
	rows, err = s.db.query(`SELECT 1 FROM settings WHERE key='compact_pending'`)
	if err != nil {
		return err
	}
	if len(rows) > 0 {
		s.compact()
	}
	return nil
}

// Caller holds the writer and reader locks (or is initializing the store).
func (s *Store) compact() {
	_, err := s.db.query(`VACUUM`)
	if err == nil {
		var rows []row
		rows, err = s.db.query(`PRAGMA wal_checkpoint(TRUNCATE)`)
		if err == nil && len(rows) > 0 && rows[0][0].(int64) != 0 {
			err = fmt.Errorf("файл БД занят другим соединением")
		}
	}
	if err == nil {
		_, err = s.db.query(`DELETE FROM settings WHERE key='compact_pending'`)
	}
	if err != nil {
		s.notice.Store(fmt.Sprintf("Данные удалены, но освободить место в файле БД пока не удалось: %v. Повторите очистку или перезапустите приложение после устранения причины.", err))
	} else {
		s.notice.Store("")
	}
}

// Clear deletes the catalog and reports only. Theme preferences and original
// audio files are retained. Once committed, compaction completes even if the
// HTTP client disconnects; a compaction failure does not undo the clear.
func (s *Store) Clear(ctx context.Context) (err error) {
	if err = s.mu.LockContext(ctx); err != nil {
		return err
	}
	defer s.mu.Unlock()
	s.readMu.Lock()
	defer s.readMu.Unlock()
	if err = ctx.Err(); err != nil {
		return err
	}
	if _, err = s.db.query(`BEGIN IMMEDIATE`); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			s.db.query(`ROLLBACK`)
		}
	}()
	for _, sql := range []string{
		`INSERT INTO settings(key,value) VALUES('track_id_floor',(SELECT coalesce(max(id),0) FROM tracks)) ON CONFLICT(key) DO UPDATE SET value=max(CAST(settings.value AS INTEGER),CAST(excluded.value AS INTEGER))`,
		`DELETE FROM tracks`,
		`DELETE FROM skipped_files`,
		`DROP TABLE IF EXISTS audio_chunks`,
		`INSERT OR REPLACE INTO settings(key,value) VALUES('compact_pending','1')`,
	} {
		if _, err = s.db.query(sql); err != nil {
			return err
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if _, err = s.db.query(`COMMIT`); err != nil {
		return err
	}
	s.compact()
	return nil
}
