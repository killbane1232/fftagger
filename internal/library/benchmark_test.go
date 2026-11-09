package library

import (
	"context"
	"fftagger/internal/store"
	"fftagger/internal/testutil"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Real ffprobe, cover extraction and SQLite metadata writes; fixtures and database
// creation are outside the timed region. Every iteration uses an empty database.
func BenchmarkScanWorkers(b *testing.B) {
	const files = 96
	tools := testutil.Tools(b)
	dir := b.TempDir()
	seed := filepath.Join(dir, "seed.flac")
	testutil.Audio(b, seed, "flac")
	track, err := tools.Probe(context.Background(), seed)
	if err != nil {
		b.Fatal(err)
	}
	_, covered, err := tools.Rewrite(context.Background(), track, track.Tags, testutil.Cover(b), true)
	if err != nil {
		b.Fatal(err)
	}
	data, err := os.ReadFile(covered)
	if err != nil {
		b.Fatal(err)
	}
	root := filepath.Join(dir, "music")
	if err = os.Mkdir(root, 0700); err != nil {
		b.Fatal(err)
	}
	for i := 0; i < files; i++ {
		if err = os.WriteFile(filepath.Join(root, fmt.Sprintf("%03d.flac", i)), data, 0600); err != nil {
			b.Fatal(err)
		}
	}
	for _, workers := range []int{1, 8} {
		b.Run(fmt.Sprintf("workers=%d", workers), func(b *testing.B) {
			b.StopTimer()
			dbDir := b.TempDir()
			for i := 0; i < b.N; i++ {
				dbPath := filepath.Join(dbDir, fmt.Sprintf("%d.sqlite", i))
				db, err := store.Open(dbPath)
				if err != nil {
					b.Fatal(err)
				}
				if _, err = db.UpdateSettings(store.SettingsPatch{ScanWorkers: &workers}); err != nil {
					db.Close()
					b.Fatal(err)
				}
				lib := &Library{Store: db, Media: tools, DBPath: dbPath}
				b.StartTimer()
				err = lib.Start(root)
				lib.wg.Wait()
				b.StopTimer()
				p := lib.Status()
				lib.Close()
				counts, countErr := db.Counts()
				db.Close()
				if err != nil || countErr != nil || p.Imported != files || p.Workers != workers || len(p.Errors) > 0 || counts.Tracks != files || counts.WithoutCover != 0 {
					b.Fatalf("scan: %+v, counts: %+v, errors: %v / %v", p, counts, err, countErr)
				}
			}
			b.ReportMetric(float64(files*b.N)/b.Elapsed().Seconds(), "files/s")
		})
	}
}
