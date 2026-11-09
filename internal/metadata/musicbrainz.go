package metadata

import (
	"context"
	"encoding/json"
	"errors"
	"fftagger/internal/media"
	"fftagger/internal/model"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

type Client struct {
	HTTP       *http.Client
	BaseURL    string
	UserAgent  string
	mu         sync.Mutex
	next       time.Time
	coverMu    sync.Mutex
	coverCache map[string]coverCacheEntry
}

func New(userAgent string) *Client {
	return &Client{HTTP: &http.Client{Timeout: 25 * time.Second}, BaseURL: "https://musicbrainz.org/ws/2", UserAgent: userAgent}
}

type Candidate struct {
	Tags        model.Tags `json:"tags"`
	RecordingID string     `json:"recording_id"`
	ReleaseID   string     `json:"release_id"`
	Score       int        `json:"score"`
	Country     string     `json:"country"`
	Length      int        `json:"length"`
	CoverStatus string     `json:"cover_status"`
}
type credit struct {
	Name   string `json:"name"`
	Join   string `json:"joinphrase"`
	Artist struct {
		Name string `json:"name"`
	} `json:"artist"`
}

func artist(c []credit) string {
	var b strings.Builder
	for _, a := range c {
		if a.Name != "" {
			b.WriteString(a.Name)
		} else {
			b.WriteString(a.Artist.Name)
		}
		b.WriteString(a.Join)
	}
	return b.String()
}
func quote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(strings.TrimSpace(s)) + `"`
}
func (c *Client) wait(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d := time.Until(c.next); d > 0 {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	c.next = time.Now().Add(1100 * time.Millisecond)
	return nil
}
func (c *Client) Search(ctx context.Context, title, name string) ([]Candidate, error) {
	if strings.TrimSpace(title) == "" && strings.TrimSpace(name) == "" {
		return nil, errors.New("введите название или исполнителя")
	}
	if len(title) > 500 || len(name) > 500 {
		return nil, errors.New("слишком длинный поисковый запрос")
	}
	q := []string{}
	if strings.TrimSpace(title) != "" {
		q = append(q, "recording:"+quote(title))
	}
	if strings.TrimSpace(name) != "" {
		q = append(q, "artist:"+quote(name))
	}
	data, err := c.request(ctx, http.MethodGet, c.BaseURL+"/recording?"+url.Values{"query": {strings.Join(q, " AND ")}, "fmt": {"json"}, "limit": {"12"}}.Encode(), 4<<20, true)
	if err != nil {
		return nil, err
	}
	var body struct {
		Recordings []struct {
			ID        string   `json:"id"`
			Title     string   `json:"title"`
			Score     int      `json:"score"`
			Length    int      `json:"length"`
			FirstDate string   `json:"first-release-date"`
			Artists   []credit `json:"artist-credit"`
			Releases  []struct {
				ID      string   `json:"id"`
				Title   string   `json:"title"`
				Date    string   `json:"date"`
				Country string   `json:"country"`
				Artists []credit `json:"artist-credit"`
			} `json:"releases"`
		} `json:"recordings"`
	}
	if err = json.Unmarshal(data, &body); err != nil {
		return nil, err
	}
	out := []Candidate{}
	for _, r := range body.Recordings {
		base := Candidate{RecordingID: r.ID, Score: r.Score, Length: r.Length, Tags: model.Tags{Title: r.Title, Artist: artist(r.Artists), Date: r.FirstDate}}
		if len(r.Releases) == 0 {
			out = append(out, base)
		}
		for i, rel := range r.Releases {
			if i >= 5 {
				break
			}
			v := base
			v.ReleaseID = rel.ID
			v.Country = rel.Country
			v.Tags.Album = rel.Title
			v.Tags.AlbumArtist = artist(rel.Artists)
			if rel.Date != "" {
				v.Tags.Date = rel.Date
			}
			out = append(out, v)
		}
	}
	if err := c.annotateCovers(ctx, out); err != nil {
		return nil, err
	}
	return out, nil
}

var mbid = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func (c *Client) Cover(ctx context.Context, release string) ([]byte, error) {
	if !mbid.MatchString(release) {
		return nil, errors.New("некорректный MusicBrainz release ID")
	}
	data, err := c.request(ctx, http.MethodGet, coverAddress(release), media.MaxCover, false)
	if err != nil {
		return nil, err
	}
	return data, media.ValidateCover(data)
}
