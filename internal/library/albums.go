package library

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fftagger/internal/model"
	"fmt"
	"strings"
	"sync"
	"time"
)

var albumFields = []string{"album", "album_artist", "date", "genre"}
var albumFieldNames = map[string]string{"album": "Альбом", "album_artist": "Исполнитель альбома", "date": "Дата / год", "genre": "Жанр", "cover": "Обложка"}

type AlbumChange struct {
	Key    string `json:"key"`
	Label  string `json:"label"`
	Before string `json:"before"`
	After  string `json:"after"`
}
type AlbumTarget struct {
	ID            int64         `json:"id"`
	Title         string        `json:"title"`
	Path          string        `json:"path"`
	Writable      bool          `json:"writable"`
	CoverWritable bool          `json:"cover_writable"`
	Changes       []AlbumChange `json:"changes"`
}
type AlbumSuggestion struct {
	ID       string        `json:"id"`
	Artist   string        `json:"artist"`
	Album    string        `json:"album"`
	SourceID int64         `json:"source_id"`
	Changes  []AlbumChange `json:"changes"`
	Targets  []AlbumTarget `json:"targets"`
}
type albumPlan struct {
	proposal     AlbumSuggestion
	created      time.Time
	source       model.Track
	coverHash    [32]byte
	coverAction  string
	targets      map[int64]model.Track
	coverChanges map[int64]bool
}
type AlbumApply struct {
	Confirm  bool     `json:"confirm"`
	Fields   []string `json:"fields"`
	TrackIDs []int64  `json:"track_ids"`
}
type AlbumItemResult struct {
	ID     int64  `json:"id"`
	Title  string `json:"title"`
	Path   string `json:"path"`
	State  string `json:"state"`
	Error  string `json:"error,omitempty"`
	Backup string `json:"backup,omitempty"`
}
type AlbumStatus struct {
	Workers     int               `json:"workers"`
	Active      int               `json:"active"`
	ActiveFiles []string          `json:"active_files"`
	ID          string            `json:"id"`
	Running     bool              `json:"running"`
	Cancelled   bool              `json:"cancelled"`
	Total       int               `json:"total"`
	Completed   int               `json:"completed"`
	Saved       int               `json:"saved"`
	Skipped     int               `json:"skipped"`
	Failed      int               `json:"failed"`
	Current     string            `json:"current"`
	Results     []AlbumItemResult `json:"results"`
}
type albumJob struct {
	status AlbumStatus
	cancel context.CancelFunc
}

func albumToken() (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(nonce[:]), nil
}
func tagValue(tags model.Tags, key string) string { return tags.Map()[key] }
func setAlbumValue(tags *model.Tags, key, value string) {
	switch key {
	case "album":
		tags.Album = value
	case "album_artist":
		tags.AlbumArtist = value
	case "date":
		tags.Date = value
	case "genre":
		tags.Genre = value
	}
}

func (l *Library) suggestAlbum(before, after model.Track) (*AlbumSuggestion, error) {
	changes := []AlbumChange{}
	for _, key := range albumFields {
		old, new := tagValue(before.Tags, key), tagValue(after.Tags, key)
		if old != new {
			changes = append(changes, AlbumChange{key, albumFieldNames[key], old, new})
		}
	}
	coverAction := "keep"
	if !bytes.Equal(before.Cover, after.Cover) {
		coverAction = "replace"
		label := "Заменить обложку"
		if len(after.Cover) == 0 {
			coverAction = "remove"
			label = "Удалить обложку"
		}
		changes = append(changes, AlbumChange{"cover", albumFieldNames["cover"], "", label})
	}
	if len(changes) == 0 {
		return nil, nil
	}
	artist, album := before.Tags.Artist, before.Tags.Album
	if strings.TrimSpace(artist) == "" || strings.TrimSpace(album) == "" {
		artist, album = after.Tags.Artist, after.Tags.Album
	}
	peers, err := l.Store.AlbumPeers(artist, album, after.ID, after.Cover)
	if err != nil {
		return nil, err
	}
	if len(peers) == 0 {
		return nil, nil
	}
	id, err := albumToken()
	if err != nil {
		return nil, err
	}
	plan := &albumPlan{created: time.Now(), source: after, coverHash: sha256.Sum256(after.Cover), coverAction: coverAction, targets: map[int64]model.Track{}, coverChanges: map[int64]bool{}}
	plan.source.Cover = nil // Artwork is loaded only when a proposal is confirmed.
	plan.proposal = AlbumSuggestion{ID: id, Artist: artist, Album: album, SourceID: after.ID, Changes: changes, Targets: []AlbumTarget{}}
	for _, peer := range peers {
		t := peer.Track
		target := AlbumTarget{ID: t.ID, Title: t.Tags.Title, Path: t.Path, Writable: t.Writable, CoverWritable: t.CoverWritable, Changes: []AlbumChange{}}
		if target.Title == "" {
			target.Title = t.Name
		}
		for _, change := range changes {
			if change.Key == "cover" {
				if !peer.CoverMatches {
					target.Changes = append(target.Changes, change)
				}
			} else if old := tagValue(t.Tags, change.Key); old != change.After {
				target.Changes = append(target.Changes, AlbumChange{change.Key, change.Label, old, change.After})
			}
		}
		if len(target.Changes) > 0 {
			plan.proposal.Targets = append(plan.proposal.Targets, target)
			plan.targets[t.ID] = t
			plan.coverChanges[t.ID] = !peer.CoverMatches
		}
	}
	if len(plan.proposal.Targets) == 0 {
		return nil, nil
	}
	l.albumMu.Lock()
	defer l.albumMu.Unlock()
	if l.albumPlans == nil {
		l.albumPlans = map[string]*albumPlan{}
	}
	for key, p := range l.albumPlans {
		if time.Since(p.created) > 30*time.Minute {
			delete(l.albumPlans, key)
		}
	}
	// Bound retained previews; they contain metadata only, not cover BLOBs.
	if len(l.albumPlans) >= 8 {
		oldest := ""
		var at time.Time
		for key, p := range l.albumPlans {
			if oldest == "" || p.created.Before(at) {
				oldest = key
				at = p.created
			}
		}
		delete(l.albumPlans, oldest)
	}
	l.albumPlans[id] = plan
	return &plan.proposal, nil
}

func (l *Library) StartAlbumApply(id string, request AlbumApply) (AlbumStatus, error) {
	if !request.Confirm {
		return AlbumStatus{}, errors.New("подтвердите изменения остальных треков альбома")
	}
	if !l.maintenance.TryRLock() {
		return AlbumStatus{}, errors.New("библиотека очищается; дождитесь завершения")
	}
	started := false
	defer func() {
		if !started {
			l.maintenance.RUnlock()
		}
	}()
	l.albumMu.Lock()
	defer l.albumMu.Unlock()
	if l.albumJob != nil && l.albumJob.status.Running {
		return AlbumStatus{}, errors.New("изменения альбома уже сохраняются")
	}
	plan := l.albumPlans[id]
	if plan == nil || time.Since(plan.created) > 30*time.Minute {
		delete(l.albumPlans, id)
		return AlbumStatus{}, errors.New("предложение устарело; сохраните трек заново")
	}
	allowed := map[string]bool{}
	for _, change := range plan.proposal.Changes {
		allowed[change.Key] = true
	}
	fields := map[string]bool{}
	for _, key := range request.Fields {
		if !allowed[key] {
			return AlbumStatus{}, fmt.Errorf("изменение %q не предлагалось", key)
		}
		fields[key] = true
	}
	if len(fields) == 0 || len(request.TrackIDs) == 0 {
		return AlbumStatus{}, errors.New("выберите изменения и хотя бы один трек")
	}
	targets := []model.Track{}
	seen := map[int64]bool{}
	for _, targetID := range request.TrackIDs {
		t, ok := plan.targets[targetID]
		if !ok {
			return AlbumStatus{}, errors.New("выбран трек вне предложенного альбома")
		}
		if seen[targetID] {
			return AlbumStatus{}, errors.New("трек выбран дважды")
		}
		seen[targetID] = true
		if !t.Writable || (fields["cover"] && plan.coverChanges[targetID] && !t.CoverWritable) {
			return AlbumStatus{}, fmt.Errorf("выбранный формат не поддерживает эти изменения: %s", t.Path)
		}
		targets = append(targets, t)
	}
	settings, err := l.Store.Settings()
	if err != nil {
		return AlbumStatus{}, err
	}
	workers, err := workerCount(settings.ScanWorkers)
	if err != nil {
		return AlbumStatus{}, err
	}
	workers = min(workers, len(targets))
	source, err := l.Store.Get(plan.source.ID)
	if err != nil {
		return AlbumStatus{}, err
	}
	if source.ModTime != plan.source.ModTime || source.Path != plan.source.Path || source.Size != plan.source.Size || source.Tags != plan.source.Tags {
		return AlbumStatus{}, errors.New("исходный трек изменился после предложения; сохраните его заново")
	}
	var cover []byte
	if fields["cover"] {
		cover, err = l.Store.Cover(source.ID)
		if err != nil {
			return AlbumStatus{}, err
		}
		if sha256.Sum256(cover) != plan.coverHash {
			return AlbumStatus{}, errors.New("обложка исходного трека изменилась; сохраните его заново")
		}
	}
	jobID, err := albumToken()
	if err != nil {
		return AlbumStatus{}, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &albumJob{status: AlbumStatus{ID: jobID, Running: true, Total: len(targets), Workers: workers, ActiveFiles: []string{}, Results: []AlbumItemResult{}}, cancel: cancel}
	l.albumJob = job
	delete(l.albumPlans, id) // A confirmation can start at most one batch.
	l.albumWG.Add(1)
	started = true
	go func() {
		defer l.albumWG.Done()
		defer l.maintenance.RUnlock()
		defer cancel()
		l.runAlbumApply(ctx, job, plan, fields, targets, cover)
	}()
	return job.status, nil
}

func (l *Library) runAlbumApply(ctx context.Context, job *albumJob, plan *albumPlan, fields map[string]bool, targets []model.Track, cover []byte) {
	process := func(expected model.Track) {
		if ctx.Err() != nil {
			return
		}
		l.albumMu.Lock()
		job.status.ActiveFiles = append(job.status.ActiveFiles, expected.Path)
		job.status.Active = len(job.status.ActiveFiles)
		job.status.Current = job.status.ActiveFiles[0]
		l.albumMu.Unlock()
		defer func() {
			l.albumMu.Lock()
			defer l.albumMu.Unlock()
			for i, path := range job.status.ActiveFiles {
				if path == expected.Path {
					job.status.ActiveFiles = append(job.status.ActiveFiles[:i], job.status.ActiveFiles[i+1:]...)
					break
				}
			}
			job.status.Active = len(job.status.ActiveFiles)
			job.status.Current = ""
			if job.status.Active > 0 {
				job.status.Current = job.status.ActiveFiles[0]
			}
		}()
		result := AlbumItemResult{ID: expected.ID, Title: expected.Tags.Title, Path: expected.Path, State: "failed"}
		if result.Title == "" {
			result.Title = expected.Name
		}
		current, err := l.Store.Get(expected.ID)
		if err == nil && (current.ModTime != expected.ModTime || current.Size != expected.Size || current.Path != expected.Path || current.Tags != expected.Tags) {
			err = errors.New("трек изменился после предложения; изменения не применены")
		}
		if err == nil {
			edit := Edit{Tags: current.Tags, ExpectedModTime: current.ModTime, CoverAction: "keep"}
			for key := range fields {
				if key != "cover" {
					setAlbumValue(&edit.Tags, key, tagValue(plan.source.Tags, key))
				}
			}
			coverChanged := false
			if fields["cover"] {
				var currentCover []byte
				currentCover, err = l.Store.Cover(current.ID)
				coverChanged = !bytes.Equal(currentCover, cover)
				if coverChanged {
					edit.CoverAction = plan.coverAction
					edit.Cover = cover
				}
			}
			if err == nil {
				if edit.Tags == current.Tags && !coverChanged {
					result.State = "skipped"
				} else {
					var saved Saved
					saved, err = l.saveTrack(ctx, current.ID, edit, false)
					if err == nil {
						result.State = "saved"
						result.Backup = saved.Backup
					}
				}
			}
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			result.Error = err.Error()
		}
		l.albumMu.Lock()
		job.status.Results = append(job.status.Results, result)
		job.status.Completed++
		switch result.State {
		case "saved":
			job.status.Saved++
		case "skipped":
			job.status.Skipped++
		default:
			job.status.Failed++
		}
		l.albumMu.Unlock()
	}
	jobs := make(chan model.Track)
	var workers sync.WaitGroup
	for i := 0; i < job.status.Workers; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for expected := range jobs {
				process(expected)
			}
		}()
	}
queue:
	for _, expected := range targets {
		select {
		case <-ctx.Done():
			break queue
		case jobs <- expected:
		}
	}
	close(jobs)
	workers.Wait()

	l.albumMu.Lock()
	defer l.albumMu.Unlock()
	job.status.Running = false
	job.status.Cancelled = ctx.Err() != nil
	job.status.Current = ""
}
func (l *Library) AlbumStatus(id string) (AlbumStatus, error) {
	l.albumMu.Lock()
	defer l.albumMu.Unlock()
	if l.albumJob == nil || l.albumJob.status.ID != id {
		return AlbumStatus{}, errors.New("операция альбома не найдена")
	}
	p := l.albumJob.status
	p.Results = append([]AlbumItemResult{}, p.Results...)
	p.ActiveFiles = append([]string{}, p.ActiveFiles...)
	return p, nil
}
func (l *Library) CancelAlbum(id string) error {
	l.albumMu.Lock()
	defer l.albumMu.Unlock()
	if l.albumJob == nil || l.albumJob.status.ID != id {
		return errors.New("операция альбома не найдена")
	}
	l.albumJob.cancel()
	return nil
}
func (l *Library) cancelAlbum() {
	l.albumMu.Lock()
	defer l.albumMu.Unlock()
	if l.albumJob != nil {
		l.albumJob.cancel()
	}
}
