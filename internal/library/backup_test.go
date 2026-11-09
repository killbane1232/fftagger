package library

import (
	"bytes"
	"context"
	"errors"
	"fftagger/internal/store"
	"fftagger/internal/testutil"
	"os"
	"path/filepath"
	"testing"
)

func TestCopyBackupPreservesDataAndRejectsOverwrite(t *testing.T) {
	dir := t.TempDir()
	source, target := filepath.Join(dir, "original"), filepath.Join(dir, "backup")
	data := bytes.Repeat([]byte("audio bytes"), 200000)
	if err := os.WriteFile(source, data, 0444); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	if err = copyBackup(context.Background(), source, target, info); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("backup did not retain the complete original")
	}
	backupInfo, err := os.Stat(target)
	if err != nil || backupInfo.Mode().Perm() != 0444 || os.SameFile(info, backupInfo) {
		t.Fatal("backup must be a separate file with original permissions")
	}
	if err = copyBackup(context.Background(), source, target, info); !errors.Is(err, os.ErrExist) {
		t.Fatalf("existing backup was not protected: %v", err)
	}
	got, err = os.ReadFile(target)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatal("collision removed or modified existing backup")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err = copyBackup(ctx, source, target+"-cancelled", info); !errors.Is(err, context.Canceled) {
		t.Fatalf("copy ignored cancellation: %v", err)
	}
	if _, err = os.Stat(target + "-cancelled"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled copy left a backup")
	}
	if err = os.Rename(source, source+"-moved"); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(source, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err = copyBackup(context.Background(), source, target+"-changed", info); err == nil {
		t.Fatal("externally replaced original was accepted")
	}
	if _, err = os.Stat(target + "-changed"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("conflict left a backup")
	}
}

func TestSaveReadOnlyFileInWritableDirectory(t *testing.T) {
	checkSaveInDirectory(t, t.TempDir(), 0444)
}

// Opt-in integration check against an actual SMB volume. It creates and removes
// only its own temporary directory, with synthetic audio; SQLite stays local.
func TestSaveOnNetworkVolume(t *testing.T) {
	root := os.Getenv("FFTAGGER_TEST_SMB_ROOT")
	if root == "" {
		t.Skip("set FFTAGGER_TEST_SMB_ROOT to a writable network volume for this integration test")
	}
	dir, err := os.MkdirTemp(root, ".fftagger-test-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			t.Errorf("cleanup test folder %s: %v", dir, err)
		}
	})
	checkSaveInDirectory(t, dir, 0700)
}

func checkSaveInDirectory(t *testing.T, dir string, mode os.FileMode) {
	t.Helper()
	tools := testutil.Tools(t)
	seed := filepath.Join(t.TempDir(), "seed.m4a")
	testutil.Audio(t, seed, "aac")
	original, err := os.ReadFile(seed)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "song.m4a")
	if err = os.WriteFile(path, original, mode); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	track, err := tools.Probe(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Put(context.Background(), &track, path); err != nil {
		if info, statErr := os.Stat(path); statErr == nil {
			t.Logf("probe size/mtime: %d/%d; current: %d/%d", track.Size, track.ModTime, info.Size(), info.ModTime().UnixNano())
		}
		t.Fatalf("initial import: %v", err)
	}
	lib := &Library{Store: db, Media: tools}
	track.Tags.Title = "Сохранение с резервной копией"
	saved, err := lib.Save(context.Background(), track.ID, Edit{Tags: track.Tags, ExpectedModTime: track.ModTime, CoverAction: "replace", Cover: testutil.Cover(t)})
	if err != nil {
		t.Fatalf("saving synthetic track: %v", err)
	}
	backup, err := os.ReadFile(saved.Backup)
	if err != nil || !bytes.Equal(backup, original) {
		t.Fatal("backup differs from the original")
	}
	fresh, err := tools.Probe(context.Background(), path)
	if err != nil || fresh.Tags.Title != track.Tags.Title || !fresh.HasCover {
		t.Fatalf("updated source: %+v %v", fresh, err)
	}
	stored, err := db.Get(track.ID)
	if err != nil || stored.Tags.Title != fresh.Tags.Title || stored.ModTime != fresh.ModTime || !stored.HasCover {
		t.Fatalf("database is out of sync: %+v %v", stored, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != mode {
		t.Fatal("file permissions were not preserved")
	}
}
