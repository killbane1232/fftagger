package library

import (
	"bytes"
	"context"
	"errors"
	"fftagger/internal/model"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Fail only FFmpeg's remux command; successful attempts still use real FFmpeg,
// ffprobe, checksum validation, backup creation and SQLite updates.
func failRemuxes(t *testing.T, l *Library, failures int) string {
	t.Helper()
	dir := t.TempDir()
	count := filepath.Join(dir, "attempts")
	wrapper := filepath.Join(dir, "ffmpeg")
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	script := fmt.Sprintf(`#!/bin/sh
case " $* " in
  *" -y "*)
    count=0
    if [ -f %s ]; then count=$(cat %s); fi
    count=$((count + 1))
    printf '%%s' "$count" > %s
    if [ "$count" -le %d ]; then echo 'simulated write failure' >&2; exit 1; fi
    ;;
esac
exec %s "$@"
`, quote(count), quote(count), quote(count), failures, quote(l.Media.FFmpeg))
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	l.Media.FFmpeg = wrapper
	return count
}

func TestSaveRetriesWithoutDuplicatingCommit(t *testing.T) {
	for _, failures := range []int{5, 6} {
		t.Run(strconv.Itoa(failures), func(t *testing.T) {
			t.Parallel()
			l, tracks := albumFixture(t)
			track := tracks[0]
			original, err := os.ReadFile(track.Path)
			if err != nil {
				t.Fatal(err)
			}
			count := failRemuxes(t, l, failures)
			tags := track.Tags
			tags.Title = "После повторов"
			start := time.Now()
			saved, err := l.Save(context.Background(), track.ID, Edit{Tags: tags, ExpectedModTime: track.ModTime, CoverAction: "keep"})
			if time.Since(start) < 5*time.Second {
				t.Fatal("retries skipped pauses")
			}
			calls, _ := os.ReadFile(count)
			if string(calls) != "6" {
				t.Fatalf("expected six attempts, got %s", calls)
			}
			backups, _ := filepath.Glob(filepath.Join(filepath.Dir(track.Path), ".fftagger-backups", "*.bak"))
			temps, _ := filepath.Glob(filepath.Join(filepath.Dir(track.Path), ".fftagger-edit-*"))
			if len(temps) != 0 {
				t.Fatal("failed attempts left temporary files")
			}
			if failures == 6 {
				if err == nil || len(backups) != 0 {
					t.Fatalf("failed save committed: %v %v", err, backups)
				}
				got, _ := os.ReadFile(track.Path)
				if !bytes.Equal(got, original) {
					t.Fatal("failed save modified original")
				}
				assertTrackTags(t, l, track, track.Tags)
				return
			}
			if err != nil || len(backups) != 1 {
				t.Fatalf("successful save: %v backups=%v", err, backups)
			}
			backup, err := os.ReadFile(saved.Backup)
			if err != nil || !bytes.Equal(backup, original) {
				t.Fatal("original backup changed")
			}
			assertTrackTags(t, l, track, tags)
		})
	}
}

func assertTrackTags(t *testing.T, l *Library, track model.Track, tags model.Tags) {
	t.Helper()
	fresh, err := l.Media.Probe(context.Background(), track.Path)
	if err != nil || fresh.Tags != tags {
		t.Fatalf("file tags: %+v %v", fresh.Tags, err)
	}
	stored, err := l.Store.Get(track.ID)
	if err != nil || stored.Tags != tags {
		t.Fatalf("database tags: %+v %v", stored.Tags, err)
	}
}

func TestSaveRetryStopsOnCancellationOrExternalChange(t *testing.T) {
	for _, cancelSave := range []bool{true, false} {
		t.Run(fmt.Sprint(cancelSave), func(t *testing.T) {
			t.Parallel()
			l, tracks := albumFixture(t)
			track := tracks[0]
			original, _ := os.ReadFile(track.Path)
			count := failRemuxes(t, l, 6)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			tags := track.Tags
			tags.Title = "Не должно сохраниться"
			go func() {
				_, err := l.Save(ctx, track.ID, Edit{Tags: tags, ExpectedModTime: track.ModTime, CoverAction: "keep"})
				done <- err
			}()
			deadline := time.Now().Add(3 * time.Second)
			for {
				if data, _ := os.ReadFile(count); string(data) == "1" {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("first attempt not reached")
				}
				time.Sleep(5 * time.Millisecond)
			}
			if cancelSave {
				cancel()
			} else {
				// Same bytes, different inode: the stale retry must still stop.
				other := track.Path + ".replacement"
				if err := os.WriteFile(other, original, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(other, track.Path); err != nil {
					t.Fatal(err)
				}
			}
			limit := 2 * time.Second
			if cancelSave {
				limit = 300 * time.Millisecond
			}
			select {
			case err := <-done:
				if err == nil || (cancelSave && !errors.Is(err, context.Canceled)) {
					t.Fatalf("unexpected error: %v", err)
				}
			case <-time.After(limit):
				t.Fatal("retry did not stop")
			}
			calls, _ := os.ReadFile(count)
			if string(calls) != "1" {
				t.Fatalf("repeated unsafe/cancelled write: %s", calls)
			}
			got, _ := os.ReadFile(track.Path)
			if !bytes.Equal(got, original) {
				t.Fatal("original changed")
			}
		})
	}
}
