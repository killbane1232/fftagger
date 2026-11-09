package server

import (
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fftagger/internal/library"
	"fftagger/internal/metadata"
	"fftagger/internal/store"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
)

//go:embed web/*
var assets embed.FS

type Server struct {
	Library      *library.Library
	Metadata     *metadata.Client
	ChooseFolder func() (string, error)
	SetTheme     func(string)
	Host         string
	Token        string
}

func New(lib *library.Library, mb *metadata.Client, host string, picker func() (string, error)) (*Server, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	return &Server{Library: lib, Metadata: mb, Host: host, Token: hex.EncodeToString(b[:]), ChooseFolder: picker}, nil
}
func (s *Server) URL() string { return "http://" + s.Host + "/?token=" + s.Token }
func send(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrSourceNotFound) {
		status = 404
	}
	send(w, status, map[string]string{"error": err.Error()})
}
func decode(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 24<<20)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	return d.Decode(v)
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		counts, err := s.Library.Store.Counts()
		if err != nil {
			failure(w, err)
			return
		}
		send(w, 200, map[string]any{"counts": counts, "scan": s.Library.Status(), "ready": s.Library.Media.Available(), "database": s.Library.DBPath, "database_warning": s.Library.Store.Notice(), "ffmpeg": s.Library.Media.FFmpeg, "ffprobe": s.Library.Media.FFprobe})
	})
	mux.HandleFunc("POST /api/library/clear", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Confirm bool `json:"confirm"`
		}
		if err := decode(w, r, &body); err != nil {
			failure(w, err)
			return
		}
		if !body.Confirm {
			failure(w, errors.New("подтвердите очистку библиотеки"))
			return
		}
		if err := s.Library.Clear(r.Context()); err != nil {
			failure(w, err)
			return
		}
		send(w, 200, map[string]any{"ok": true, "warning": s.Library.Store.Notice()})
	})
	mux.HandleFunc("GET /api/settings", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Library.Store.Settings()
		if err != nil {
			failure(w, err)
			return
		}
		send(w, 200, v)
	})
	mux.HandleFunc("PUT /api/settings", func(w http.ResponseWriter, r *http.Request) {
		var patch store.SettingsPatch
		if err := decode(w, r, &patch); err != nil {
			failure(w, err)
			return
		}
		v, err := s.Library.Store.UpdateSettings(patch)
		if err != nil {
			failure(w, err)
			return
		}
		if s.SetTheme != nil && patch.Theme != nil {
			s.SetTheme(v.Theme)
		}
		send(w, 200, v)
	})
	mux.HandleFunc("GET /api/sources", func(w http.ResponseWriter, r *http.Request) {
		sources, err := s.Library.Store.Sources()
		if err != nil {
			failure(w, err)
			return
		}
		send(w, 200, sources)
	})
	mux.HandleFunc("POST /api/sources", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Path string `json:"path"`
		}
		if err := decode(w, r, &body); err != nil {
			failure(w, err)
			return
		}
		source, err := s.Library.AddSource(body.Path)
		if err != nil {
			failure(w, err)
			return
		}
		send(w, 200, source)
	})
	mux.HandleFunc("DELETE /api/sources/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			failure(w, err)
			return
		}
		if err = s.Library.RemoveSource(id); err != nil {
			failure(w, err)
			return
		}
		send(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/skipped", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		offset, _ := strconv.Atoi(q.Get("offset"))
		p, err := s.Library.Store.ListSkipped(q.Get("q"), q.Get("kind"), offset)
		if err != nil {
			failure(w, err)
			return
		}
		send(w, 200, p)
	})
	mux.HandleFunc("POST /api/pick", func(w http.ResponseWriter, r *http.Request) {
		path, err := s.ChooseFolder()
		if err != nil {
			failure(w, err)
			return
		}
		send(w, 200, map[string]string{"path": path})
	})
	mux.HandleFunc("POST /api/scan", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Path string `json:"path"`
		}
		if err := decode(w, r, &body); err != nil {
			failure(w, err)
			return
		}
		if body.Path == "" {
			failure(w, errors.New("выберите папку"))
			return
		}
		if err := s.Library.Start(body.Path); err != nil {
			failure(w, err)
			return
		}
		send(w, 202, s.Library.Status())
	})
	mux.HandleFunc("POST /api/scan/cancel", func(w http.ResponseWriter, r *http.Request) {
		s.Library.Cancel()
		send(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/tracks", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		offset, _ := strconv.Atoi(q.Get("offset"))
		p, err := s.Library.Store.List(store.Query{Search: q.Get("q"), Artist: q.Get("artist"), Album: q.Get("album"), Cover: q.Get("cover"), Sort: q.Get("sort"), Order: q.Get("order"), Offset: offset, Limit: 100})
		if err != nil {
			failure(w, err)
			return
		}
		send(w, 200, p)
	})
	mux.HandleFunc("GET /api/tracks/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			failure(w, err)
			return
		}
		t, err := s.Library.Store.Get(id)
		if err != nil {
			failure(w, err)
			return
		}
		send(w, 200, t)
	})
	mux.HandleFunc("GET /api/tracks/{id}/cover", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			failure(w, err)
			return
		}
		data, err := s.Library.Store.Cover(id)
		if err != nil {
			failure(w, err)
			return
		}
		if len(data) == 0 {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", http.DetectContentType(data))
		w.Write(data)
	})
	mux.HandleFunc("PUT /api/tracks/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			failure(w, err)
			return
		}
		var edit library.Edit
		if err = decode(w, r, &edit); err != nil {
			failure(w, err)
			return
		}
		saved, err := s.Library.Save(r.Context(), id, edit)
		if err != nil {
			failure(w, err)
			return
		}
		send(w, 200, saved)
	})
	mux.HandleFunc("DELETE /api/tracks/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			failure(w, err)
			return
		}
		var request library.DeleteRequest
		if err = decode(w, r, &request); err != nil {
			failure(w, err)
			return
		}
		deleted, err := s.Library.Delete(r.Context(), id, request)
		if err != nil {
			failure(w, err)
			return
		}
		send(w, 200, deleted)
	})
	mux.HandleFunc("GET /api/search", func(w http.ResponseWriter, r *http.Request) {
		v, err := s.Metadata.Search(r.Context(), r.URL.Query().Get("title"), r.URL.Query().Get("artist"))
		if err != nil {
			failure(w, err)
			return
		}
		send(w, 200, v)
	})
	mux.HandleFunc("POST /api/album-proposals/{id}/apply", func(w http.ResponseWriter, r *http.Request) {
		var body library.AlbumApply
		if err := decode(w, r, &body); err != nil {
			failure(w, err)
			return
		}
		status, err := s.Library.StartAlbumApply(r.PathValue("id"), body)
		if err != nil {
			failure(w, err)
			return
		}
		send(w, 202, status)
	})
	mux.HandleFunc("GET /api/album-jobs/{id}", func(w http.ResponseWriter, r *http.Request) {
		status, err := s.Library.AlbumStatus(r.PathValue("id"))
		if err != nil {
			failure(w, err)
			return
		}
		send(w, 200, status)
	})
	mux.HandleFunc("POST /api/album-jobs/{id}/cancel", func(w http.ResponseWriter, r *http.Request) {
		if err := s.Library.CancelAlbum(r.PathValue("id")); err != nil {
			failure(w, err)
			return
		}
		send(w, 200, map[string]bool{"ok": true})
	})
	mux.HandleFunc("GET /api/artwork/{release}", func(w http.ResponseWriter, r *http.Request) {
		data, err := s.Metadata.Cover(r.Context(), r.PathValue("release"))
		if err != nil {
			failure(w, err)
			return
		}
		w.Header().Set("Content-Type", http.DetectContentType(data))
		w.Write(data)
	})
	files, _ := fs.Sub(assets, "web")
	mux.Handle("GET /", http.FileServer(http.FS(files)))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: blob:; style-src 'self'; script-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		if r.Host != s.Host {
			http.Error(w, "Forbidden host", 403)
			return
		}
		if r.URL.Path == "/" && r.Method == "GET" && subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(s.Token)) == 1 {
			http.SetCookie(w, &http.Cookie{Name: "fftagger_session", Value: s.Token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		cookie, err := r.Cookie("fftagger_session")
		if err != nil || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(s.Token)) != 1 {
			http.Error(w, "Unauthorized", 401)
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+s.Host {
			http.Error(w, "Forbidden origin", 403)
			return
		}
		if site := r.Header.Get("Sec-Fetch-Site"); site == "cross-site" {
			http.Error(w, "Forbidden site", 403)
			return
		}
		if r.Method != "GET" && r.Method != "HEAD" && !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			http.Error(w, "JSON required", 415)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
