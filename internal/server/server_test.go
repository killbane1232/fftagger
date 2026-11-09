package server

import (
	"context"
	"encoding/json"
	"errors"
	"fftagger/internal/library"
	"fftagger/internal/metadata"
	"fftagger/internal/model"
	"fftagger/internal/store"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalSessionAndOriginProtection(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s, err := New(&library.Library{Store: db}, metadata.New("test"), "127.0.0.1:1234", func() (string, error) { return "/music", nil })
	if err != nil {
		t.Fatal(err)
	}
	handler := s.Handler()
	for _, tc := range []struct {
		name, method, path, host, origin, contentType string
		cookie                                        bool
		want                                          int
	}{{"no session", "GET", "/api/status", "127.0.0.1:1234", "", "", false, 401}, {"bad host", "GET", "/api/status", "evil.example", "", "", true, 403}, {"cross origin", "POST", "/api/pick", "127.0.0.1:1234", "https://evil.example", "application/json", true, 403}, {"form post", "POST", "/api/pick", "127.0.0.1:1234", "", "application/x-www-form-urlencoded", true, 415}, {"valid", "GET", "/api/status", "127.0.0.1:1234", "", "", true, 200}, {"index", "GET", "/", "127.0.0.1:1234", "", "", true, 200}} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "http://"+tc.host+tc.path, nil)
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Content-Type", tc.contentType)
			if tc.cookie {
				r.AddCookie(&http.Cookie{Name: "fftagger_session", Value: s.Token})
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			if w.Code != tc.want {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}
		})
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("GET", s.URL(), nil))
	if w.Code != 303 || len(w.Result().Cookies()) != 1 || !w.Result().Cookies()[0].HttpOnly {
		t.Fatal("session bootstrap failed")
	}
}

func TestDeleteTrackRequiresExactConfirmationAndDeletesFile(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	path := filepath.Join(dir, "song.flac")
	if err = os.WriteFile(path, []byte("test audio"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	track := model.Track{Path: path, Size: info.Size(), ModTime: info.ModTime().UnixNano()}
	if err = db.Put(context.Background(), &track, path); err != nil {
		t.Fatal(err)
	}
	lib := &library.Library{Store: db}
	defer lib.Close()
	s, err := New(lib, metadata.New("test"), "127.0.0.1:1234", nil)
	if err != nil {
		t.Fatal(err)
	}
	request := library.DeleteRequest{Confirm: true, ExpectedPath: path, ExpectedModTime: track.ModTime}
	valid, _ := json.Marshal(request)
	for _, tc := range []struct {
		body    string
		session bool
		want    int
	}{
		{string(valid), false, 401}, {`{}`, true, 400}, {`{"confirm":false}`, true, 400}, {`{"confirm":true}`, true, 400}, {string(valid), true, 200},
	} {
		r := httptest.NewRequest("DELETE", fmt.Sprintf("http://%s/api/tracks/%d", s.Host, track.ID), strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/json")
		if tc.session {
			r.AddCookie(&http.Cookie{Name: "fftagger_session", Value: s.Token})
		}
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("delete %s: %d %s", tc.body, w.Code, w.Body.String())
		}
		_, fileErr := os.Lstat(path)
		_, dbErr := db.Get(track.ID)
		if tc.want == 200 {
			if !errors.Is(fileErr, os.ErrNotExist) || !errors.Is(dbErr, store.ErrNotFound) {
				t.Fatalf("deletion incomplete: file=%v db=%v", fileErr, dbErr)
			}
		} else if fileErr != nil || dbErr != nil {
			t.Fatal("unconfirmed request removed file or record")
		}
	}
}

func TestClearRequiresConfirmationAndKeepsAudioFiles(t *testing.T) {
	dir := t.TempDir()
	db, err := store.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	path := filepath.Join(dir, "source.flac")
	if err = os.WriteFile(path, []byte("audio source"), 0600); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	track := model.Track{Path: path, Size: info.Size(), ModTime: info.ModTime().UnixNano()}
	if err = db.Put(context.Background(), &track, path); err != nil {
		t.Fatal(err)
	}
	s, err := New(&library.Library{Store: db}, metadata.New("test"), "127.0.0.1:1234", nil)
	if err != nil {
		t.Fatal(err)
	}
	handler := s.Handler()
	for _, tc := range []struct {
		body string
		want int
	}{{`{}`, 400}, {`{"confirm":false}`, 400}, {`{"confirm":true}`, 200}} {
		r := httptest.NewRequest("POST", "http://"+s.Host+"/api/library/clear", strings.NewReader(tc.body))
		r.Header.Set("Content-Type", "application/json")
		r.AddCookie(&http.Cookie{Name: "fftagger_session", Value: s.Token})
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Fatalf("clear %s: %d %s", tc.body, w.Code, w.Body.String())
		}
		counts, err := db.Counts()
		if err != nil {
			t.Fatal(err)
		}
		wantCount := int64(1)
		if tc.want == 200 {
			wantCount = 0
		}
		if counts.Tracks != wantCount {
			t.Fatalf("unexpected count after %s: %+v", tc.body, counts)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "audio source" {
		t.Fatal("clear touched source audio")
	}
}
