package store

import (
	"context"
	"path/filepath"
	"testing"
)

func TestCoverFiltersAndPersistentPreferences(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "library.sqlite")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.Close() }()
	with := fixture(t, s, dir, "covered", "Север", "Альбом", []byte("a"))
	without := fixture(t, s, dir, "uncovered", "Север", "Альбом", []byte("b"))
	without.Cover = nil
	without.HasCover = false
	if err = s.Put(context.Background(), &without, without.Path); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		cover string
		want  int64
	}{{"present", with.ID}, {"missing", without.ID}} {
		p, e := s.List(Query{Cover: tc.cover, Artist: "сев", Album: "аль", Sort: "album"})
		if e != nil || p.Total != 1 || p.Tracks[0].ID != tc.want {
			t.Fatalf("filter %s: %+v %v", tc.cover, p, e)
		}
	}
	if err = s.SetSettings(Settings{Theme: "dark"}); err != nil {
		t.Fatal(err)
	}
	if err = s.SetSettings(Settings{Theme: "invalid"}); err == nil {
		t.Fatal("invalid theme accepted")
	}
	if err = s.RecordSkipped("/music", "/music/Ошибка.mp3", "read_error", "Не читается", "broken"); err != nil {
		t.Fatal(err)
	}
	// Reopening upgrades a v1 database without changing the audio library.
	if _, err = s.db.query(`PRAGMA user_version=1`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := s.Settings()
	if err != nil || settings.Theme != "dark" {
		t.Fatalf("theme not persisted: %+v %v", settings, err)
	}
	counts, err := s.Counts()
	if err != nil || counts != (Counts{Tracks: 2, WithoutCover: 1, Skipped: 1}) {
		t.Fatalf("counts after reopen: %+v %v", counts, err)
	}
	without.Cover = []byte("new cover")
	without.HasCover = true
	if err = s.Put(context.Background(), &without, without.Path); err != nil {
		t.Fatal(err)
	}
	p, err := s.List(Query{Cover: "missing"})
	if err != nil || p.Total != 0 {
		t.Fatal("saved artwork did not leave the missing-art filter")
	}
}

func TestSkippedReportsFilterAndReplaceOnlySelectedRoot(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, v := range []struct{ root, path, kind string }{{"/a", "/a/Ошибка.mp3", "read_error"}, {"/a", "/a/notes.txt", "not_audio"}, {"/b", "/b/other.flac", "read_error"}} {
		if err = s.RecordSkipped(v.root, v.path, v.kind, "reason", "detail"); err != nil {
			t.Fatal(err)
		}
	}
	if err = s.RecordSkipped("/a", "/a/Ошибка.mp3", "read_error", "updated", "new detail"); err != nil {
		t.Fatal(err)
	}
	p, err := s.ListSkipped("ОШИБКА", "read_error", 0)
	if err != nil || p.Total != 1 || p.Files[0].Reason != "updated" {
		t.Fatalf("search/upsert: %+v %v", p, err)
	}
	if err = s.ClearSkipped("/a"); err != nil {
		t.Fatal(err)
	}
	p, err = s.ListSkipped("", "", 0)
	if err != nil || p.Total != 1 || p.Files[0].Root != "/b" {
		t.Fatalf("other folder report was lost: %+v %v", p, err)
	}
}
