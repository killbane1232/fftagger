package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
)

func TestSettingsAndSourcesSurviveRestartAndCatalogClear(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "library.sqlite")
	s, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if s != nil {
			s.Close()
		}
	})
	dark, workers := "dark", 5
	// Independent UI controls must not replace one another's preferences.
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); _, e := s.UpdateSettings(SettingsPatch{Theme: &dark}); errs <- e }()
	go func() { defer wg.Done(); _, e := s.UpdateSettings(SettingsPatch{ScanWorkers: &workers}); errs <- e }()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	settings, err := s.Settings()
	if err != nil || settings.Theme != "dark" || settings.ScanWorkers != 5 {
		t.Fatalf("lost settings: %+v %v", settings, err)
	}
	invalid, light := 17, "light"
	if _, err = s.UpdateSettings(SettingsPatch{Theme: &light, ScanWorkers: &invalid}); err == nil {
		t.Fatal("invalid workers accepted")
	}
	settings, _ = s.Settings()
	if settings.Theme != "dark" || settings.ScanWorkers != 5 {
		t.Fatal("invalid patch partially changed settings")
	}
	first, err := s.RememberSource("/Volumes/Offline/Music", false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.RememberSource("/Users/example/Music", false)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := s.RememberSource(first.Path, true)
	if err != nil || updated.ID != first.ID || updated.LastScanAt == "" || updated.AddedAt != first.AddedAt {
		t.Fatalf("source update: %+v %v", updated, err)
	}
	if _, err = s.RememberSource(first.Path, false); err != nil {
		t.Fatal(err)
	}
	if err = s.Clear(context.Background()); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	settings, err = s.Settings()
	if err != nil || settings.Theme != "dark" || settings.ScanWorkers != 5 {
		t.Fatalf("preferences not persisted: %+v %v", settings, err)
	}
	sources, err := s.Sources()
	if err != nil || len(sources) != 2 || sources[0] != updated || sources[1].ID != second.ID {
		t.Fatalf("sources not persisted/deduplicated: %+v %v", sources, err)
	}
	if err = s.RemoveSource(first.ID); err != nil {
		t.Fatal(err)
	}
	s.Close()
	s, err = Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	sources, err = s.Sources()
	if err != nil || len(sources) != 1 || sources[0].ID != second.ID {
		t.Fatal("removed source reappeared")
	}
}
