package metadata

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (fn transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

type brokenBody struct{}

func (brokenBody) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func (brokenBody) Close() error             { return nil }

func TestNetworkRetriesTransportStatusAndInterruptedBody(t *testing.T) {
	t.Parallel()
	c := New("FFTagger-test")
	var calls []time.Time
	c.HTTP.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		calls = append(calls, time.Now())
		switch len(calls) {
		case 1:
			return nil, errors.New("connection reset")
		case 2:
			return response(503, "busy"), nil
		case 3:
			return response(429, "rate limit"), nil
		case 4:
			return &http.Response{StatusCode: 200, Body: brokenBody{}}, nil
		case 5:
			return response(408, "timeout"), nil
		default:
			return response(200, "ok"), nil
		}
	})
	got, err := c.request(context.Background(), http.MethodGet, "https://musicbrainz.org/test", 1024, true)
	if err != nil || string(got) != "ok" || len(calls) != 6 {
		t.Fatalf("result=%s calls=%d err=%v", got, len(calls), err)
	}
	for i := 1; i < len(calls); i++ {
		if calls[i].Sub(calls[i-1]) < 1100*time.Millisecond {
			t.Fatal("retry bypassed MusicBrainz rate limit")
		}
	}
}

func TestNetworkStopsAfterFiveRetriesAndDoesNotRetryNotFound(t *testing.T) {
	t.Parallel()
	c := New("FFTagger-test")
	calls := 0
	status := 503
	c.HTTP.Transport = transportFunc(func(r *http.Request) (*http.Response, error) { calls++; return response(status, ""), nil })
	if _, err := c.request(context.Background(), http.MethodGet, "https://coverartarchive.org/test", 1024, false); err == nil || calls != 6 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	status = 404
	calls = 0
	if _, err := c.request(context.Background(), http.MethodHead, "https://coverartarchive.org/test", 0, false); !errors.Is(err, errNoCover) || calls != 1 {
		t.Fatalf("missing cover retried: calls=%d err=%v", calls, err)
	}
}

func TestSearchCoverLabelsOrderingDedupAndCache(t *testing.T) {
	c := New("FFTagger-test")
	const available = "00000000-0000-0000-0000-000000000001"
	const missing = "00000000-0000-0000-0000-000000000002"
	const unknown = "00000000-0000-0000-0000-000000000003"
	var mu sync.Mutex
	checks := map[string]int{}
	c.HTTP.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "musicbrainz.org" {
			return response(200, fmt.Sprintf(`{"recordings":[
{"id":"r1","title":"More relevant","score":100,"releases":[{"id":%q,"title":"No cover"},{"id":%q,"title":"Check failed"}]},
{"id":"r2","title":"With cover","score":80,"releases":[{"id":%q,"title":"Front cover"}]},
{"id":"r3","title":"Other recording","score":70,"releases":[{"id":%q,"title":"Same release"}]},
{"id":"r4","title":"No release","score":60}]}`, missing, unknown, available, available)), nil
		}
		if r.Method != http.MethodHead {
			t.Errorf("availability downloaded an image: %s", r.Method)
		}
		mu.Lock()
		checks[r.URL.Path]++
		mu.Unlock()
		switch {
		case strings.Contains(r.URL.Path, available):
			return response(200, ""), nil
		case strings.Contains(r.URL.Path, missing):
			return response(404, ""), nil
		default:
			return response(403, ""), nil
		}
	})
	for pass := 0; pass < 2; pass++ {
		out, err := c.Search(context.Background(), "Title", "Artist")
		if err != nil || len(out) != 5 {
			t.Fatalf("search: %+v %v", out, err)
		}
		if out[0].RecordingID != "r2" || out[1].RecordingID != "r3" || out[0].CoverStatus != "available" || out[1].CoverStatus != "available" {
			t.Fatalf("covers not first/relevance lost: %+v", out)
		}
		if out[2].CoverStatus != "missing" || out[3].CoverStatus != "unknown" || out[4].CoverStatus != "missing" {
			t.Fatalf("incorrect cover labels: %+v", out)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if checks["/release/"+available+"/front-500"] != 1 || checks["/release/"+missing+"/front-500"] != 1 || checks["/release/"+unknown+"/front-500"] != 2 {
		t.Fatalf("bad dedup/cache: %+v", checks)
	}
}

func TestCoverChecksBoundedAndCancelled(t *testing.T) {
	c := New("FFTagger-test")
	var active, peak atomic.Int32
	entered := make(chan struct{}, 20)
	c.HTTP.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		entered <- struct{}{}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	candidates := make([]Candidate, 20)
	for i := range candidates {
		candidates[i].ReleaseID = fmt.Sprintf("00000000-0000-0000-0000-%012d", i)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.annotateCovers(ctx, candidates) }()
	for i := 0; i < 4; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("checks not parallel")
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("cover checks did not stop")
	}
	if active.Load() != 0 || peak.Load() != 4 {
		t.Fatalf("pool lifetime/bound: active=%d peak=%d", active.Load(), peak.Load())
	}
}
