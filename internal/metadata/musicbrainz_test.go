package metadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type localTransport struct{ handler http.Handler }

func (t localTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	w := httptest.NewRecorder()
	t.handler.ServeHTTP(w, r)
	return w.Result(), nil
}

func TestSearchEncodesQueryAndKeepsReleaseChoices(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != "FFTagger-test/1.0 (test@example.org)" {
			t.Error("missing user agent")
		}
		if got := r.URL.Query().Get("query"); !strings.Contains(got, `recording:"A \"title\""`) {
			t.Errorf("bad escaped query: %s", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"recordings":[{"id":"rec","title":"Title","score":99,"length":120000,"artist-credit":[{"name":"A","joinphrase":" & "},{"artist":{"name":"B"}}],"releases":[{"id":"one","title":"First","date":"2020"},{"id":"two","title":"Second","date":"2021"}]}]}`))
	})
	c := New("FFTagger-test/1.0 (test@example.org)")
	c.HTTP.Transport = localTransport{handler: handler}
	r, err := c.Search(context.Background(), `A "title"`, "Artist")
	if err != nil {
		t.Fatal(err)
	}
	if len(r) != 2 || r[0].Tags.Artist != "A & B" || r[1].Tags.Album != "Second" {
		t.Fatalf("release choices: %+v", r)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err = c.Search(ctx, "title", ""); err == nil {
		t.Fatal("rate limiter did not honor cancellation")
	}
}
