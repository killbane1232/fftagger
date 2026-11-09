package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func databaseSize(t *testing.T, path string) int64 {
	t.Helper()
	var total int64
	for _, name := range []string{path, path + "-wal"} {
		info, err := os.Stat(name)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		total += info.Size()
	}
	return total
}

func TestLegacyAudioMigrationPreservesCatalogAndReclaimsSpace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s != nil {
			s.Close()
		}
	})
	track := fixture(t, s, dir, "track.flac", "Исполнитель", "Альбом", []byte("original audio"))
	if err = s.SetSettings(Settings{Theme: "dark"}); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordSkipped(dir, filepath.Join(dir, "broken.mp3"), "read_error", "Ошибка", "подробности"); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`CREATE TABLE audio_chunks(track_id INTEGER NOT NULL REFERENCES tracks(id) ON DELETE CASCADE,seq INTEGER NOT NULL,data BLOB NOT NULL,PRIMARY KEY(track_id,seq))`,
		`INSERT INTO audio_chunks(track_id,seq,data) SELECT id,0,zeroblob(12582912) FROM tracks`,
		`PRAGMA user_version=2`,
		`PRAGMA wal_checkpoint(TRUNCATE)`,
	} {
		if _, err = s.db.query(q); err != nil {
			t.Fatal(err)
		}
	}
	before := databaseSize(t, path)
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if s.Notice() != "" {
		t.Fatal(s.Notice())
	}
	got, err := s.Get(track.ID)
	if err != nil || got.Path != track.Path || got.Tags != track.Tags {
		t.Fatalf("migration lost metadata: %+v %v", got, err)
	}
	cover, err := s.Cover(track.ID)
	if err != nil || !bytes.Equal(cover, track.Cover) {
		t.Fatal("migration lost artwork")
	}
	settings, err := s.Settings()
	if err != nil || settings.Theme != "dark" {
		t.Fatal("migration lost preferences")
	}
	counts, err := s.Counts()
	if err != nil || counts.Tracks != 1 || counts.Skipped != 1 {
		t.Fatal("migration lost catalog or report")
	}
	rows, err := s.db.query(`SELECT name FROM sqlite_master WHERE type='table' AND name='audio_chunks'`)
	if err != nil || len(rows) != 0 {
		t.Fatal("legacy audio remains")
	}
	if after := databaseSize(t, path); after >= before/4 {
		t.Fatalf("space not reclaimed: %d -> %d", before, after)
	}
	data, err := os.ReadFile(track.Path)
	if err != nil || string(data) != "original audio" {
		t.Fatal("migration touched the source")
	}
}

func TestClearPreservesFilesAndThemeAndDoesNotReuseIDs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "library.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	track := fixture(t, s, dir, "song.flac", "Artist", "Album", []byte("source"))
	track.Cover = bytes.Repeat([]byte("art"), 1000000)
	if err = s.Put(context.Background(), &track, track.Path); err != nil {
		t.Fatal(err)
	}
	if err = s.SetSettings(Settings{Theme: "dark"}); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordSkipped(dir, "skip", "not_audio", "Не аудио", ""); err != nil {
		t.Fatal(err)
	}
	before := databaseSize(t, path)
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err = s.Clear(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled clear succeeded")
	}
	if _, err = s.Get(track.ID); err != nil {
		t.Fatal("cancelled clear removed data")
	}
	if err = s.Clear(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s.Notice() != "" {
		t.Fatal(s.Notice())
	}
	counts, err := s.Counts()
	if err != nil || counts != (Counts{}) {
		t.Fatalf("not empty: %+v %v", counts, err)
	}
	if _, err = s.Get(track.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("old track survived")
	}
	if after := databaseSize(t, path); after >= before/4 {
		t.Fatalf("clear did not compact: %d -> %d", before, after)
	}
	settings, err := s.Settings()
	if err != nil || settings.Theme != "dark" {
		t.Fatal("clear removed theme")
	}
	data, err := os.ReadFile(track.Path)
	if err != nil || string(data) != "source" {
		t.Fatal("clear changed audio")
	}
	newTrack := fixture(t, s, dir, "next.flac", "Next", "Album", []byte("next source"))
	if newTrack.ID <= track.ID {
		t.Fatal("clear reused a stale track ID")
	}
	if _, err = s.Get(track.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("stale ID now addresses another file")
	}
}
