package metadata

import (
	"context"
	"errors"
	"fftagger/internal/retry"
	"fmt"
	"io"
	"net/http"
	"sort"
	"sync"
	"time"
)

var errNoCover = errors.New("в источнике нет обложки для этого издания")

func coverAddress(release string) string {
	return "https://coverartarchive.org/release/" + release + "/front-500"
}

func (c *Client) request(ctx context.Context, method, address string, limit int64, musicBrainz bool) ([]byte, error) {
	var data []byte
	err := retry.Do(ctx, func() error {
		if musicBrainz {
			if err := c.wait(ctx); err != nil {
				return err
			}
		}
		attemptCtx := ctx
		if method == http.MethodHead {
			// Availability checks never download artwork and use a shorter timeout.
			var cancel context.CancelFunc
			attemptCtx, cancel = context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
		}
		req, err := http.NewRequestWithContext(attemptCtx, method, address, nil)
		if err != nil {
			return retry.Permanent(err)
		}
		req.Header.Set("User-Agent", c.UserAgent)
		req.Header.Set("Accept", "application/json, image/jpeg, image/png")
		res, err := c.HTTP.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode == http.StatusNotFound && !musicBrainz {
			return retry.Permanent(errNoCover)
		}
		if res.StatusCode != http.StatusOK {
			err = fmt.Errorf("источник вернул HTTP %d", res.StatusCode)
			if res.StatusCode == http.StatusRequestTimeout || res.StatusCode == http.StatusTooManyRequests || res.StatusCode >= 500 {
				return err
			}
			return retry.Permanent(err)
		}
		if method == http.MethodHead {
			return nil
		}
		body, err := io.ReadAll(io.LimitReader(res.Body, limit+1))
		if err != nil {
			return err
		}
		if int64(len(body)) > limit {
			return retry.Permanent(errors.New("слишком большой ответ источника"))
		}
		data = body
		return nil
	})
	return data, err
}

type coverCacheEntry struct {
	status  string
	expires time.Time
}

func (c *Client) coverStatus(ctx context.Context, release string) string {
	if release == "" {
		return "missing"
	}
	if !mbid.MatchString(release) {
		return "unknown"
	}
	c.coverMu.Lock()
	cached, ok := c.coverCache[release]
	c.coverMu.Unlock()
	if ok && time.Now().Before(cached.expires) {
		return cached.status
	}
	_, err := c.request(ctx, http.MethodHead, coverAddress(release), 0, false)
	status := "available"
	if errors.Is(err, errNoCover) {
		status = "missing"
	} else if err != nil {
		return "unknown" // A failed lookup is not evidence of missing artwork.
	}
	c.coverMu.Lock()
	if c.coverCache == nil || len(c.coverCache) >= 512 {
		c.coverCache = make(map[string]coverCacheEntry)
	}
	c.coverCache[release] = coverCacheEntry{status: status, expires: time.Now().Add(15 * time.Minute)}
	c.coverMu.Unlock()
	return status
}

func (c *Client) annotateCovers(ctx context.Context, candidates []Candidate) error {
	// Check each edition once per search, with a bounded pool and no image BLOBs.
	indices := make(map[string][]int)
	for i, candidate := range candidates {
		indices[candidate.ReleaseID] = append(indices[candidate.ReleaseID], i)
	}
	jobs := make(chan string)
	var workers sync.WaitGroup
	for i := 0; i < min(4, len(indices)); i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for release := range jobs {
				if ctx.Err() != nil {
					return
				}
				status := c.coverStatus(ctx, release)
				for _, index := range indices[release] {
					candidates[index].CoverStatus = status
				}
			}
		}()
	}
queue:
	for release := range indices {
		select {
		case <-ctx.Done():
			break queue
		case jobs <- release:
		}
	}
	close(jobs)
	workers.Wait()
	if err := ctx.Err(); err != nil {
		return err
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		a, b := candidates[i], candidates[j]
		if (a.CoverStatus == "available") != (b.CoverStatus == "available") {
			return a.CoverStatus == "available"
		}
		return a.Score > b.Score
	})
	return nil
}
