package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestCancelledImportDoesNotWaitForAnotherWriter(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tr := fixture(t, s, dir, "a", "artist", "album", []byte("audio"))
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.Put(ctx, &tr, tr.Path) }()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("unexpected cancellation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled import stuck waiting for SQLite writer")
	}
}
