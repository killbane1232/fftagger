package library

import (
	"context"
	"fftagger/internal/store"
	"fftagger/internal/testutil"
	"os"
	"path/filepath"
	"testing"
)

func TestSourcePathsAndPersistentWorkerSetting(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "music")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	testutil.Audio(t, filepath.Join(root, "song.flac"), "flac")
	dbPath := filepath.Join(dir, "library.sqlite")
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if db != nil {
			db.Close()
		}
	})
	l := &Library{Store: db, Media: testutil.Tools(t)}
	t.Cleanup(func() { l.Close() })
	source, err := l.AddSource(root)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err = os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	duplicate, err := l.AddSource(alias + "/.")
	if err != nil || duplicate.ID != source.ID {
		t.Fatalf("alias added duplicate source: %+v %v", duplicate, err)
	}
	for _, bad := range []string{"", filepath.Join(dir, "missing"), filepath.Join(root, "song.flac")} {
		if _, err = l.AddSource(bad); err == nil {
			t.Fatalf("invalid source accepted: %q", bad)
		}
	}
	workers := 3
	if _, err = db.UpdateSettings(store.SettingsPatch{ScanWorkers: &workers}); err != nil {
		t.Fatal(err)
	}
	if err = l.Start(alias); err != nil {
		t.Fatal(err)
	}
	l.wg.Wait()
	if p := l.Status(); p.Workers != 3 || p.Imported != 1 || len(p.Errors) > 0 {
		t.Fatalf("scan ignored settings: %+v", p)
	}
	sources, err := db.Sources()
	if err != nil || len(sources) != 1 || sources[0].LastScanAt == "" {
		t.Fatalf("scan did not remember source: %+v %v", sources, err)
	}
	workers = 1
	if _, err = db.UpdateSettings(store.SettingsPatch{ScanWorkers: &workers}); err != nil {
		t.Fatal(err)
	}
	if l.Status().Workers != 3 {
		t.Fatal("setting changed the completed/active scan snapshot")
	}
	db.Close()
	db, err = store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	l = &Library{Store: db, Media: testutil.Tools(t)}
	if err = l.Start(root); err != nil {
		t.Fatal(err)
	}
	l.wg.Wait()
	if p := l.Status(); p.Workers != 1 || p.Unchanged != 1 {
		t.Fatalf("new process ignored saved workers: %+v", p)
	}
	if err = l.RemoveSource(source.ID); err != nil {
		t.Fatal(err)
	}
	page, err := db.List(store.Query{})
	if err != nil || page.Total != 1 {
		t.Fatal("removing source removed imported tracks")
	}
	if _, err = os.Stat(filepath.Join(root, "song.flac")); err != nil {
		t.Fatal("removing source removed audio")
	}
	// A disconnected source remains listed, and an unsuccessful rescan never
	// deletes it. Listing does not touch the filesystem.
	if _, err = l.AddSource(root); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(root, root+"-offline"); err != nil {
		t.Fatal(err)
	}
	if err = l.Start(root); err == nil {
		t.Fatal("unavailable source scanned successfully")
	}
	sources, err = db.Sources()
	if err != nil || len(sources) != 1 {
		t.Fatal("unavailable source disappeared")
	}
	if err = l.Clear(context.Background()); err != nil {
		t.Fatal(err)
	}
	sources, err = db.Sources()
	if err != nil || len(sources) != 1 {
		t.Fatal("catalog clear removed sources")
	}
}
