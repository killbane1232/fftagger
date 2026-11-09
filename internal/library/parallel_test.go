package library

import (
	"context"
	"errors"
	"fftagger/internal/model"
	"fftagger/internal/store"
	"fftagger/internal/testutil"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func scanFixture(t testing.TB, n int) (*Library, string) {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "music")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("%03d.flac", i)), []byte("audio payload"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	db, err := store.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return &Library{Store: db}, root
}
func probeFixture(ctx context.Context, path string) (model.Track, error) {
	f, err := os.Stat(path)
	if err != nil {
		return model.Track{}, err
	}
	return model.Track{Path: path, Name: filepath.Base(path), Size: f.Size(), ModTime: f.ModTime().UnixNano(), Tags: model.Tags{Title: filepath.Base(path)}}, nil
}
func waitScan(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("scan did not finish")
	}
}

func TestScanRunsBoundedConcurrentProbesAndJoinsWorkers(t *testing.T) {
	const workers = 4
	l, root := scanFixture(t, 17)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{}, 32)
	release := make(chan struct{})
	done := make(chan struct{})
	var active, peak atomic.Int32
	probe := func(ctx context.Context, path string) (model.Track, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		entered <- struct{}{}
		select {
		case <-release:
		case <-ctx.Done():
			return model.Track{}, ctx.Err()
		}
		return probeFixture(ctx, path)
	}
	go func() { l.scan(ctx, root, workers, probe); close(done) }()
	defer func() { cancel(); waitScan(t, done) }()
	for i := 0; i < workers; i++ {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("probes still run serially")
		}
	}
	p := l.Status()
	if p.Active != workers || len(p.ActiveFiles) != workers {
		t.Fatalf("bad active progress: %+v", p)
	}
	p.ActiveFiles[0] = "mutated"
	if l.Status().ActiveFiles[0] == "mutated" {
		t.Fatal("Status exposed shared state")
	}
	close(release)
	waitScan(t, done)
	if peak.Load() != workers || active.Load() != 0 {
		t.Fatalf("worker bound/lifetime: peak=%d active=%d", peak.Load(), active.Load())
	}
	p = l.Status()
	if p.Imported != 17 || p.Visited != 17 || p.Active != 0 || len(p.Errors) > 0 {
		t.Fatalf("bad completed progress: %+v", p)
	}
	page, err := l.Store.List(store.Query{})
	if err != nil || page.Total != 17 {
		t.Fatalf("lost/duplicate imports: %+v %v", page, err)
	}
	if len(l.files.entries) != 0 {
		t.Fatal("per-file locks leaked")
	}
}

func TestCancelStopsQueuedAndRunningFiles(t *testing.T) {
	l, root := scanFixture(t, 100)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{}, 100)
	done := make(chan struct{})
	probe := func(ctx context.Context, path string) (model.Track, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return model.Track{}, ctx.Err()
	}
	go func() { l.scan(ctx, root, 4, probe); close(done) }()
	defer func() { cancel(); waitScan(t, done) }()
	for i := 0; i < 4; i++ {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("workers did not start")
		}
	}
	cancel()
	waitScan(t, done)
	p := l.Status()
	if p.Active != 0 || p.Visited != 4 || p.Skipped != 0 || len(p.Errors) > 0 {
		t.Fatalf("cancel processed queued files or reported spurious errors: %+v", p)
	}
	page, err := l.Store.List(store.Query{})
	if err != nil || page.Total != 0 {
		t.Fatal("cancelled probes were imported")
	}
	// A new scan after cancellation must not inherit blocked locks or workers.
	l.scan(context.Background(), root, 4, probeFixture)
	page, err = l.Store.List(store.Query{})
	if err != nil || page.Total != 100 {
		t.Fatal("scan did not recover after cancellation")
	}
}

func TestClearCancelsAndJoinsWorkersBeforeRemovingCatalog(t *testing.T) {
	l, root := scanFixture(t, 30)
	ctx, cancel := context.WithCancel(context.Background())
	l.cancel = cancel
	l.progress = Progress{Running: true, Root: root}
	entered := make(chan struct{}, 4)
	probe := func(ctx context.Context, path string) (model.Track, error) {
		entered <- struct{}{}
		<-ctx.Done()
		return model.Track{}, ctx.Err()
	}
	l.wg.Add(1)
	go func() { defer l.wg.Done(); l.scan(ctx, root, 4, probe) }()
	defer l.Close()
	for i := 0; i < 4; i++ {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("workers did not start")
		}
	}
	if err := l.Clear(context.Background()); err != nil {
		t.Fatal(err)
	}
	p := l.Status()
	if p.Running || p.Root != "" || p.Active != 0 {
		t.Fatalf("progress not reset: %+v", p)
	}
	counts, err := l.Store.Counts()
	if err != nil || counts != (store.Counts{}) {
		t.Fatalf("catalog not empty: %+v %v", counts, err)
	}
	if len(l.files.entries) != 0 {
		t.Fatal("workers survived clear")
	}
	// A subsequent import remains usable after the maintenance lock is released.
	l.scan(context.Background(), root, 4, probeFixture)
	counts, err = l.Store.Counts()
	if err != nil || counts.Tracks != 30 {
		t.Fatal("cannot scan after clearing")
	}
}

func TestFileLocksSerializeSameFileAndCancelWaiters(t *testing.T) {
	var locks fileLocks
	release, err := locks.acquire(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	other, err := locks.acquire(context.Background(), "b")
	if err != nil {
		t.Fatal(err)
	}
	other()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err = locks.acquire(ctx, "a"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("same path did not wait: %v", err)
	}
	release()
	if len(locks.entries) != 0 {
		t.Fatal("cancelled waiter leaked a lock")
	}
}

func TestSaveCannotRaceScanOfSameFile(t *testing.T) {
	tools := testutil.Tools(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "song.flac")
	testutil.Audio(t, path, "flac")
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
		t.Fatal(err)
	}
	// Force a re-probe while retaining valid audio.
	changed := time.Now().Add(time.Second)
	if err = os.Chtimes(path, changed, changed); err != nil {
		t.Fatal(err)
	}
	l := &Library{Store: db, Media: tools}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	probe := func(ctx context.Context, path string) (model.Track, error) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return model.Track{}, ctx.Err()
		}
		return tools.Probe(ctx, path)
	}
	go func() { l.scan(ctx, dir, 2, probe); close(done) }()
	defer func() { cancel(); waitScan(t, done) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("scan did not start")
	}
	saveCtx, stop := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer stop()
	_, err = l.Save(saveCtx, track.ID, Edit{Tags: track.Tags, ExpectedModTime: track.ModTime, CoverAction: "keep"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("save did not wait for the scanner: %v", err)
	}
	close(release)
	waitScan(t, done)
	fresh, err := db.Get(track.ID)
	if err != nil {
		t.Fatal(err)
	}
	fresh.Tags.Title = "Edited after scan"
	if _, err = l.Save(context.Background(), track.ID, Edit{Tags: fresh.Tags, ExpectedModTime: fresh.ModTime, CoverAction: "keep"}); err != nil {
		t.Fatal(err)
	}
	got, err := db.Get(track.ID)
	if err != nil || got.Tags.Title != fresh.Tags.Title {
		t.Fatal("scan overwrote edit")
	}
}
