package library

import (
	"bytes"
	"context"
	"fftagger/internal/store"
	"fftagger/internal/testutil"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanSaveBackupConflictAndDeduplication(t *testing.T) {
	dir := t.TempDir()
	music := filepath.Join(dir, "music")
	os.MkdirAll(filepath.Join(music, "nested"), 0700)
	path := filepath.Join(music, "nested", "no-extension")
	testutil.Audio(t, path, "flac", "-f", "flac")
	original, _ := os.ReadFile(path)
	os.WriteFile(filepath.Join(music, "notes.txt"), []byte("not audio"), 0600)
	os.Symlink(path, filepath.Join(music, "link.flac"))
	dbPath := filepath.Join(dir, "library.sqlite")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	lib := &Library{Store: db, Media: testutil.Tools(t), DBPath: dbPath}
	defer lib.Close()
	scan := func() {
		t.Helper()
		if err := lib.Start(music); err != nil {
			t.Fatal(err)
		}
		lib.wg.Wait()
		p := lib.Status()
		if p.Cancelled || len(p.Errors) > 0 {
			t.Fatalf("scan errors: %+v", p)
		}
	}
	scan()
	page, err := db.List(store.Query{})
	if err != nil || page.Total != 1 {
		t.Fatalf("scan: %+v %v", page, err)
	}
	tr := page.Tracks[0]
	skipped, err := db.ListSkipped("", "", 0)
	if err != nil || skipped.Total != 2 || lib.Status().Skipped != 2 {
		t.Fatalf("skipped report: %+v %v", skipped, err)
	}
	tr.Tags.Title = "Сохранённый трек"
	saved, err := lib.Save(context.Background(), tr.ID, Edit{Tags: tr.Tags, ExpectedModTime: tr.ModTime, CoverAction: "replace", Cover: testutil.Cover(t)})
	if err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(saved.Backup)
	if err != nil || !bytes.Equal(backup, original) {
		t.Fatal("original backup missing or changed")
	}
	fresh, err := lib.Media.Probe(context.Background(), path)
	if err != nil || fresh.Tags.Title != tr.Tags.Title || !fresh.HasCover {
		t.Fatalf("file did not update: %+v %v", fresh, err)
	}
	scan()
	page, _ = db.List(store.Query{})
	if page.Total != 1 || lib.Status().Unchanged != 1 {
		t.Fatal("rescan imported backup or duplicate")
	}
	future := time.Now().Add(time.Hour)
	os.Chtimes(path, future, future)
	if _, err = lib.Save(context.Background(), tr.ID, Edit{Tags: tr.Tags, ExpectedModTime: saved.Track.ModTime, CoverAction: "keep"}); err == nil {
		t.Fatal("external changes were overwritten")
	}
}

func TestRescanRemovesRepairedFileFromSkippedReport(t *testing.T) {
	dir := t.TempDir()
	music := filepath.Join(dir, "music")
	os.MkdirAll(music, 0700)
	broken := filepath.Join(music, "broken.mp3")
	os.WriteFile(broken, []byte("broken"), 0600)
	db, err := store.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	lib := &Library{Store: db, Media: testutil.Tools(t)}
	defer lib.Close()
	if err = lib.Start(music); err != nil {
		t.Fatal(err)
	}
	lib.wg.Wait()
	p, err := db.ListSkipped("", "read_error", 0)
	if err != nil || p.Total != 1 || p.Files[0].Detail == "" {
		t.Fatalf("missing decode error: %+v %v", p, err)
	}
	testutil.Audio(t, broken, "libmp3lame")
	if err = lib.Start(music); err != nil {
		t.Fatal(err)
	}
	lib.wg.Wait()
	p, err = db.ListSkipped("", "", 0)
	if err != nil || p.Total != 0 {
		t.Fatalf("repaired file still skipped: %+v %v", p, err)
	}
	tracks, err := db.List(store.Query{})
	if err != nil || tracks.Total != 1 {
		t.Fatalf("repaired file not imported: %+v %v", tracks, err)
	}
}
