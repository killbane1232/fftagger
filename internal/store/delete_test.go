package store

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestDeleteCatalogIsAtomicAndDoesNotReuseIDs(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	other := fixture(t, s, dir, "other.flac", "Artist", "Album", []byte("other"))
	track := fixture(t, s, dir, "delete.flac", "Artist", "Album", []byte("delete"))
	if err := s.RecordSkipped(dir, track.Path, "read_error", "Error", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSettings(Settings{Theme: "dark", ScanWorkers: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RememberSource(dir, false); err != nil {
		t.Fatal(err)
	}
	called := 0
	want := errors.New("staging failed")
	err = s.Delete(context.Background(), track, func() error { called++; return want })
	if !errors.Is(err, want) || called != 1 {
		t.Fatalf("stage failure: %v calls=%d", err, called)
	}
	cover, err := s.Cover(track.ID)
	if err != nil || !bytes.Equal(cover, track.Cover) {
		t.Fatal("rollback lost cover")
	}
	counts, err := s.Counts()
	if err != nil || counts.Tracks != 2 || counts.Skipped != 1 {
		t.Fatalf("partial transaction: %+v %v", counts, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = s.Delete(ctx, track, func() error { t.Fatal("cancelled deletion staged a file"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	stale := track
	stale.ModTime++
	if err = s.Delete(context.Background(), stale, func() error { t.Fatal("stale deletion staged a file"); return nil }); err == nil {
		t.Fatal("stale track accepted")
	}
	if err = s.Delete(context.Background(), track, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Get(track.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("track remained")
	}
	if _, err = s.Cover(track.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("artwork remained")
	}
	counts, err = s.Counts()
	if err != nil || counts.Tracks != 1 || counts.Skipped != 0 {
		t.Fatalf("delete counts: %+v %v", counts, err)
	}
	if _, err = s.Get(other.ID); err != nil {
		t.Fatal("another track removed")
	}
	settings, err := s.Settings()
	if err != nil || settings.Theme != "dark" || settings.ScanWorkers != 3 {
		t.Fatal("preferences lost")
	}
	sources, err := s.Sources()
	if err != nil || len(sources) != 1 {
		t.Fatal("sources lost")
	}
	newTrack := fixture(t, s, dir, "next.flac", "Artist", "Album", []byte("next"))
	if newTrack.ID <= track.ID {
		t.Fatal("deleted ID reused")
	}
	if _, err = os.Stat(track.Path); err != nil {
		t.Fatal("store touched file outside staging callback")
	}
}

func TestDeleteCommitFailureKeepsMetadata(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	track := fixture(t, s, dir, "delete.flac", "Artist", "Album", []byte("delete"))
	for _, sql := range []string{`CREATE TABLE deletion_guard(track_id INTEGER REFERENCES tracks(id) DEFERRABLE INITIALLY DEFERRED)`, `INSERT INTO deletion_guard SELECT id FROM tracks`} {
		if _, err = s.db.query(sql); err != nil {
			t.Fatal(err)
		}
	}
	staged := false
	err = s.Delete(context.Background(), track, func() error { staged = true; return nil })
	if !staged || err == nil {
		t.Fatalf("commit failure not exercised: %v staged=%t", err, staged)
	}
	if _, err = s.Get(track.ID); err != nil {
		t.Fatal("failed commit lost track")
	}
	cover, err := s.Cover(track.ID)
	if err != nil || !bytes.Equal(cover, track.Cover) {
		t.Fatal("failed commit lost cover")
	}
}
