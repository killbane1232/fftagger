package media

import (
	"bytes"
	"context"
	"errors"
	"fftagger/internal/model"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRewriteExplainsDirectoryWritePermission(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root bypasses mode-based permission restrictions")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "song.flac")
	original := []byte("source must stay untouched")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	_, _, err := (Tools{}).Rewrite(context.Background(), model.Track{Path: path, Format: "flac", Writable: true}, model.Tags{}, nil, false)
	if !errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), "нет права создавать файлы") || !strings.Contains(err.Error(), dir) {
		t.Fatalf("missing actionable permission error: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, original) {
		t.Fatal("permission failure modified the original")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatal("permission failure left temporary files")
	}
}
