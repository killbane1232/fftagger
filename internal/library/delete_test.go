package library

import (
	"bytes"
	"context"
	"errors"
	"fftagger/internal/model"
	"fftagger/internal/store"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func deletionFixture(t *testing.T) (*Library, model.Track) {
	t.Helper()
	dir := t.TempDir()
	dbpath := filepath.Join(dir, "db.sqlite")
	db, err := store.Open(dbpath)
	if err != nil {
		t.Fatal(err)
	}
	l := &Library{Store: db, DBPath: dbpath}
	t.Cleanup(func() { l.Close(); db.Close() })
	path := filepath.Join(dir, "song.unknown")
	if err = os.WriteFile(path, []byte("original audio"), 0444); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	track := model.Track{Path: path, Name: filepath.Base(path), Size: info.Size(), ModTime: info.ModTime().UnixNano(), Tags: model.Tags{Title: "Удаляемый трек"}, HasCover: true, Cover: []byte("art")}
	if err = db.Put(context.Background(), &track, path); err != nil {
		t.Fatal(err)
	}
	return l, track
}
func deletionRequest(track model.Track) DeleteRequest {
	return DeleteRequest{Confirm: true, ExpectedPath: track.Path, ExpectedModTime: track.ModTime}
}
func assertDeletionIntact(t *testing.T, l *Library, track model.Track) {
	t.Helper()
	data, err := os.ReadFile(track.Path)
	if err != nil || string(data) != "original audio" {
		t.Fatalf("original changed: %v", err)
	}
	stored, err := l.Store.Get(track.ID)
	if err != nil || stored.Tags != track.Tags {
		t.Fatal("catalog changed")
	}
}
func assertNoStagedDeletion(t *testing.T, track model.Track) {
	t.Helper()
	paths, err := filepath.Glob(filepath.Join(filepath.Dir(track.Path), ".fftagger-delete-*"))
	if err != nil || len(paths) != 0 {
		t.Fatalf("staged files left: %v %v", paths, err)
	}
}

func TestDeleteRequiresConfirmationAndRemovesReadOnlyTrack(t *testing.T) {
	l, track := deletionFixture(t)
	request := deletionRequest(track)
	request.Confirm = false
	if _, err := l.Delete(context.Background(), track.ID, request); err == nil {
		t.Fatal("unconfirmed deletion accepted")
	}
	assertDeletionIntact(t, l, track)
	request = deletionRequest(track)
	request.ExpectedPath += ".different"
	if _, err := l.Delete(context.Background(), track.ID, request); err == nil {
		t.Fatal("wrong path accepted")
	}
	request = deletionRequest(track)
	request.ExpectedModTime++
	if _, err := l.Delete(context.Background(), track.ID, request); err == nil {
		t.Fatal("stale confirmation accepted")
	}
	assertDeletionIntact(t, l, track)
	backup := filepath.Join(filepath.Dir(track.Path), ".fftagger-backups")
	if err := os.Mkdir(backup, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backup, "old.bak"), []byte("previous backup"), 0600); err != nil {
		t.Fatal(err)
	}
	result, err := l.Delete(context.Background(), track.ID, deletionRequest(track))
	if err != nil || result.AlreadyMissing || result.Warning != "" {
		t.Fatalf("delete: %+v %v", result, err)
	}
	if _, err = os.Lstat(track.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("file still exists")
	}
	if _, err = l.Store.Get(track.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("track still exists")
	}
	if _, err = l.Store.Cover(track.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("artwork still exists")
	}
	if data, err := os.ReadFile(filepath.Join(backup, "old.bak")); err != nil || string(data) != "previous backup" {
		t.Fatal("previous backup removed")
	}
	assertNoStagedDeletion(t, track)
}

func TestDeleteMissingFileAndRejectsChangedOrSymlink(t *testing.T) {
	for _, kind := range []string{"missing", "changed", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			l, track := deletionFixture(t)
			switch kind {
			case "missing":
				if err := os.Remove(track.Path); err != nil {
					t.Fatal(err)
				}
			case "changed":
				future := time.Now().Add(time.Hour)
				if err := os.Chtimes(track.Path, future, future); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Rename(track.Path, track.Path+".real"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(track.Path+".real", track.Path); err != nil {
					t.Fatal(err)
				}
			}
			result, err := l.Delete(context.Background(), track.ID, deletionRequest(track))
			if kind == "missing" {
				if err != nil || !result.AlreadyMissing {
					t.Fatalf("missing file: %+v %v", result, err)
				}
				if _, err = l.Store.Get(track.ID); !errors.Is(err, store.ErrNotFound) {
					t.Fatal("missing file entry remained")
				}
			} else {
				if err == nil {
					t.Fatal("changed file was deleted")
				}
				assertDeletionIntact(t, l, track)
			}
			assertNoStagedDeletion(t, track)
		})
	}
}

func TestDeleteRestoresFileOnDatabaseCommitFailure(t *testing.T) {
	l, track := deletionFixture(t)
	sqlite, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 CLI required for commit fault injection")
	}
	query := `CREATE TABLE deletion_guard(track_id INTEGER REFERENCES tracks(id) DEFERRABLE INITIALLY DEFERRED); INSERT INTO deletion_guard SELECT id FROM tracks;`
	if out, err := exec.Command(sqlite, l.DBPath, query).CombinedOutput(); err != nil {
		t.Fatalf("fault injection: %v %s", err, out)
	}
	if _, err = l.Delete(context.Background(), track.ID, deletionRequest(track)); err == nil {
		t.Fatal("commit failure hidden")
	}
	assertDeletionIntact(t, l, track)
	info, err := os.Stat(track.Path)
	if err != nil || info.ModTime().UnixNano() != track.ModTime || info.Mode().Perm() != 0444 {
		t.Fatal("rollback changed source attributes")
	}
	cover, err := l.Store.Cover(track.ID)
	if err != nil || !bytes.Equal(cover, track.Cover) {
		t.Fatal("rollback lost artwork")
	}
	assertNoStagedDeletion(t, track)
}

func TestDeleteWaitsForFileLockAndHonorsCancellation(t *testing.T) {
	l, track := deletionFixture(t)
	release, err := l.files.acquire(context.Background(), track.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = l.Delete(ctx, track.ID, deletionRequest(track))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("file lock ignored: %v", err)
	}
	assertDeletionIntact(t, l, track)
	assertNoStagedDeletion(t, track)
}

func TestStagedDeleteRollbackNeverOverwritesRecreatedSource(t *testing.T) {
	l, track := deletionFixture(t)
	var staged stagedDeletion
	if err := staged.prepare(context.Background(), track); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(track.Path, []byte("external replacement"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := staged.restore(); err == nil {
		t.Fatal("rollback overwrote new source")
	}
	if data, _ := os.ReadFile(track.Path); string(data) != "external replacement" {
		t.Fatal("external file lost")
	}
	if data, _ := os.ReadFile(staged.path); string(data) != "original audio" {
		t.Fatal("recovery copy lost")
	}
	if _, err := l.Store.Get(track.ID); err != nil {
		t.Fatal(err)
	}
}

func TestQueuedScanIgnoresConfirmedDeletion(t *testing.T) {
	l, track := deletionFixture(t)
	release, err := l.files.acquire(context.Background(), track.Path)
	if err != nil {
		t.Fatal(err)
	}
	release = sync.OnceFunc(release)
	defer release()
	done := make(chan struct{})
	go func() {
		l.scan(context.Background(), filepath.Dir(track.Path), 1, func(ctx context.Context, path string) (model.Track, error) {
			return model.Track{}, errors.New("unexpected probe")
		})
		close(done)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		l.files.mu.Lock()
		users := l.files.entries[track.Path].users
		l.files.mu.Unlock()
		if users == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("scan did not queue behind file lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// Complete the same staged transaction used by Delete while holding its
	// per-file lock, with the scanner known to be waiting behind it.
	var staged stagedDeletion
	if err := l.Store.Delete(context.Background(), track, func() error { return staged.prepare(context.Background(), track) }); err != nil {
		t.Fatal(err)
	}
	if err := staged.cleanup(); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("queued scan did not finish")
	}
	if _, err = l.Store.Get(track.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatal("deleted track reimported")
	}
	report, err := l.Store.ListSkipped("", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range report.Files {
		if item.Path == track.Path || strings.Contains(item.Path, ".fftagger-delete-") {
			t.Fatalf("deleted track in skipped report: %+v", item)
		}
	}
	if p := l.Status(); len(p.Errors) != 0 || p.Imported != 0 {
		t.Fatalf("queued scan treated deletion as error: %+v", p)
	}
}

func TestDeletePermissionFailureKeepsFileAndCatalog(t *testing.T) {
	t.Parallel()
	l, track := deletionFixture(t)
	dir := filepath.Dir(track.Path)
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	if f, err := os.CreateTemp(dir, "permission-check-*"); err == nil {
		f.Close()
		os.Remove(f.Name())
		t.Skip("filesystem does not enforce directory permissions")
	}
	if _, err := l.Delete(context.Background(), track.ID, deletionRequest(track)); err == nil {
		t.Fatal("permission failure hidden")
	}
	assertDeletionIntact(t, l, track)
	assertNoStagedDeletion(t, track)
}

func TestScanLeavesStagedRecoveryFileUntouched(t *testing.T) {
	l, track := deletionFixture(t)
	dir := filepath.Join(filepath.Dir(track.Path), ".fftagger-delete-recovery")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "original.flac")
	if err := os.WriteFile(path, []byte("recovery audio"), 0600); err != nil {
		t.Fatal(err)
	}
	l.scan(context.Background(), filepath.Dir(track.Path), 2, func(ctx context.Context, path string) (model.Track, error) {
		t.Errorf("unexpected probe: %s", path)
		return model.Track{}, errors.New("not audio")
	})
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "recovery audio" {
		t.Fatal("scan touched recovery file")
	}
	counts, err := l.Store.Counts()
	if err != nil || counts.Tracks != 1 || counts.Skipped != 0 {
		t.Fatalf("recovery file was scanned: %+v %v", counts, err)
	}
}
