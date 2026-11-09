package store

import (
	"context"
	"errors"
	"fftagger/internal/model"
)

// Delete prepares the catalog transaction before staging the file removal.
// If staging or commit fails the caller restores the staged file. Once staged,
// finish the transaction even if the HTTP request has been cancelled.
func (s *Store) Delete(ctx context.Context, expected model.Track, stage func() error) (err error) {
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
	rows, err := s.db.query(`SELECT id,document FROM tracks WHERE id=?`, expected.ID)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return ErrNotFound
	}
	current, err := decode(rows[0])
	if err != nil {
		return err
	}
	if current.Path != expected.Path || current.ModTime != expected.ModTime || current.Size != expected.Size || current.Tags != expected.Tags {
		return errors.New("трек изменился перед удалением; откройте его заново")
	}
	// Even deleting the highest/last ID must never make it reusable by a stale
	// editor, deletion confirmation or album proposal.
	if _, err = s.db.query(`INSERT INTO settings(key,value) VALUES('track_id_floor',?) ON CONFLICT(key) DO UPDATE SET value=max(CAST(settings.value AS INTEGER),CAST(excluded.value AS INTEGER))`, expected.ID); err != nil {
		return err
	}
	if _, err = s.db.query(`DELETE FROM skipped_files WHERE path=?`, expected.Path); err != nil {
		return err
	}
	if _, err = s.db.query(`DELETE FROM tracks WHERE id=?`, expected.ID); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = stage(); err != nil {
		return err
	}
	_, err = s.db.query(`COMMIT`)
	return err
}
