package store

import (
	"bytes"
	"context"
	"fftagger/internal/model"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T, s *Store, dir, name, artist, album string, payload []byte) model.Track {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, payload, 0600); err != nil {
		t.Fatal(err)
	}
	stat, _ := os.Stat(path)
	tr := model.Track{Path: path, Name: name, Size: stat.Size(), ModTime: stat.ModTime().UnixNano(), Tags: model.Tags{Title: name, Artist: artist, Album: album}, Cover: []byte("cover"), HasCover: true}
	if err := s.Put(context.Background(), &tr, path); err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestWALReaderDoesNotWaitForImportTransaction(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	fixture(t, s, dir, "a", "old", "album", []byte("audio"))
	s.mu.Lock()
	if _, err = s.db.query(`BEGIN IMMEDIATE`); err != nil {
		s.mu.Unlock()
		t.Fatal(err)
	}
	defer func() { s.db.query(`ROLLBACK`); s.mu.Unlock() }()
	if _, err = s.db.query(`UPDATE tracks SET artist_key='new'`); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		p, e := s.List(Query{Artist: "old"})
		if e == nil && p.Total != 1 {
			e = fmt.Errorf("reader did not see committed snapshot: %d", p.Total)
		}
		result <- e
	}()
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("library reader blocked behind audio import")
	}
}
func TestMetadataOnlyIndexesAndUnicodeQueries(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "library.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	payload := bytes.Repeat([]byte("audio"), 600000)
	tr := fixture(t, s, dir, "track1", "Ёлка", "Весна", payload)
	fixture(t, s, dir, "track2", "Аквариум", "Зима", []byte("second"))
	r, err := s.db.query(`SELECT name FROM sqlite_master WHERE type='table' AND name='audio_chunks'`)
	if err != nil || len(r) != 0 {
		t.Fatal("audio storage table still exists")
	}
	pages, err := s.db.query(`PRAGMA page_count`)
	if err != nil {
		t.Fatal(err)
	}
	pageSize, err := s.db.query(`PRAGMA page_size`)
	if err != nil {
		t.Fatal(err)
	}
	if pages[0][0].(int64)*pageSize[0][0].(int64) >= int64(len(payload)) {
		t.Fatal("catalog unexpectedly stores the audio payload")
	}
	cover, err := s.Cover(tr.ID)
	if err != nil || !bytes.Equal(cover, tr.Cover) {
		t.Fatal("artwork was not retained")
	}
	for _, q := range []Query{{Artist: "ёл", Sort: "artist"}, {Album: "вес", Sort: "album"}, {Search: "ЁЛК"}} {
		p, e := s.List(q)
		if e != nil || p.Total != 1 || p.Tracks[0].ID != tr.ID {
			t.Fatalf("query %+v: %+v %v", q, p, e)
		}
	}
	asc, _ := s.List(Query{Sort: "album"})
	desc, _ := s.List(Query{Sort: "album", Order: "desc"})
	if asc.Tracks[0].ID == desc.Tracks[0].ID {
		t.Fatal("sort direction ignored")
	}
	for _, idx := range []string{"idx_tracks_artist", "idx_tracks_album"} {
		rows, e := s.db.query(`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, idx)
		if e != nil || len(rows) != 1 {
			t.Fatalf("index missing: %s", idx)
		}
	}
	for _, col := range []string{"artist", "album"} {
		rows, e := s.db.query(`EXPLAIN QUERY PLAN SELECT id FROM tracks WHERE `+col+`_key>=? AND `+col+`_key<? ORDER BY `+col+`_key,title_key,id`, "а", "я")
		if e != nil {
			t.Fatal(e)
		}
		if !strings.Contains(rows[0][3].(string), "idx_tracks_"+col+"_key") {
			t.Fatalf("index not used: %v", rows)
		}
	}
}
func TestCancelledImportPreservesTags(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tr := fixture(t, s, dir, "a", "old", "old", []byte("original"))
	tr.Tags.Artist = "new"
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = s.Put(ctx, &tr, tr.Path); err == nil {
		t.Fatal("cancelled import succeeded")
	}
	got, err := s.Get(tr.ID)
	if err != nil || got.Tags.Artist != "old" {
		t.Fatalf("rollback failed: %+v %v", got, err)
	}
	data, err := os.ReadFile(tr.Path)
	if err != nil || string(data) != "original" {
		t.Fatal("source audio was changed")
	}
}
