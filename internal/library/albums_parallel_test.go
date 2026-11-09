package library

import (
	"context"
	"fftagger/internal/store"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestAlbumWorkersUseSettingAndAllowIndependentWrites(t *testing.T) {
	for _, workers := range []int{1, 2} {
		t.Run(fmt.Sprint(workers), func(t *testing.T) {
			l, tracks := albumFixture(t)
			if err := l.Store.SetSettings(store.Settings{Theme: "system", ScanWorkers: workers}); err != nil {
				t.Fatal(err)
			}
			saved := saveAlbumChange(t, l, tracks[0], false)
			releaseFirst, err := l.files.acquire(context.Background(), tracks[1].Path)
			if err != nil {
				t.Fatal(err)
			}
			releaseFirst = sync.OnceFunc(releaseFirst)
			defer releaseFirst()
			releaseSecond, err := l.files.acquire(context.Background(), tracks[2].Path)
			if err != nil {
				t.Fatal(err)
			}
			releaseSecond = sync.OnceFunc(releaseSecond)
			defer releaseSecond()
			job, err := l.StartAlbumApply(saved.AlbumSuggestion.ID, AlbumApply{Confirm: true, Fields: []string{"date"}, TrackIDs: []int64{tracks[1].ID, tracks[2].ID}})
			if err != nil {
				t.Fatal(err)
			}
			defer l.CancelAlbum(job.ID)
			p := awaitAlbumCondition(t, l, job.ID, func(p AlbumStatus) bool { return p.Active == workers })
			if p.Workers != workers || len(p.ActiveFiles) != workers || p.Completed != 0 {
				t.Fatalf("worker setting ignored: %+v", p)
			}
			p.ActiveFiles[0] = "mutated"
			p, _ = l.AlbumStatus(job.ID)
			if p.ActiveFiles[0] == "mutated" {
				t.Fatal("progress exposed shared slice")
			}
			// Changing preferences must not resize the already running pool.
			otherWorkers := 3 - workers
			if err := l.Store.SetSettings(store.Settings{Theme: "system", ScanWorkers: otherWorkers}); err != nil {
				t.Fatal(err)
			}
			p, _ = l.AlbumStatus(job.ID)
			if p.Workers != workers {
				t.Fatal("running pool changed size")
			}
			releaseSecond()
			if workers == 2 {
				p = awaitAlbumCondition(t, l, job.ID, func(p AlbumStatus) bool { return p.Saved == 1 })
				if p.Results[0].ID != tracks[2].ID || p.Active != 1 || !p.Running {
					t.Fatalf("independent track blocked: %+v", p)
				}
			} else {
				// Only the first, locked file can be active with one worker.
				if p.ActiveFiles[0] != tracks[1].Path {
					t.Fatalf("queue ignored worker bound: %+v", p)
				}
			}
			releaseFirst()
			p = waitAlbum(t, l, job.ID)
			if p.Saved != 2 || p.Failed != 0 || p.Active != 0 || len(p.ActiveFiles) != 0 {
				t.Fatalf("unfinished workers: %+v", p)
			}
			for _, track := range tracks[1:3] {
				tags := track.Tags
				tags.Date = "2026"
				assertTrackTags(t, l, track, tags)
			}
		})
	}
}

func TestAlbumCancellationJoinsAllWorkers(t *testing.T) {
	l, tracks := albumFixture(t)
	if err := l.Store.SetSettings(store.Settings{Theme: "system", ScanWorkers: 16}); err != nil {
		t.Fatal(err)
	}
	saved := saveAlbumChange(t, l, tracks[0], false)
	for _, track := range tracks[1:3] {
		release, err := l.files.acquire(context.Background(), track.Path)
		if err != nil {
			t.Fatal(err)
		}
		defer release()
	}
	job, err := l.StartAlbumApply(saved.AlbumSuggestion.ID, AlbumApply{Confirm: true, Fields: []string{"album"}, TrackIDs: []int64{tracks[1].ID, tracks[2].ID}})
	if err != nil {
		t.Fatal(err)
	}
	p := awaitAlbumCondition(t, l, job.ID, func(p AlbumStatus) bool { return p.Active == 2 })
	if p.Workers != 2 {
		t.Fatalf("pool exceeds target count: %+v", p)
	}
	if err := l.CancelAlbum(job.ID); err != nil {
		t.Fatal(err)
	}
	p = waitAlbum(t, l, job.ID)
	if p.Running || !p.Cancelled || p.Completed != 0 || p.Active != 0 {
		t.Fatalf("cancelled workers still running: %+v", p)
	}
	for _, track := range tracks[1:3] {
		assertTrackTags(t, l, track, track.Tags)
	}
}

func awaitAlbumCondition(t *testing.T, l *Library, id string, condition func(AlbumStatus) bool) AlbumStatus {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for {
		p, err := l.AlbumStatus(id)
		if err != nil {
			t.Fatal(err)
		}
		if condition(p) {
			return p
		}
		if time.Now().After(deadline) {
			t.Fatalf("album progress timed out: %+v", p)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
