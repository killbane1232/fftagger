package library

import (
	"bytes"
	"context"
	"fftagger/internal/model"
	"fftagger/internal/store"
	"fftagger/internal/testutil"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func albumFixture(t *testing.T) (*Library, []model.Track) {
	t.Helper()
	dir := t.TempDir()
	tools := testutil.Tools(t)
	db, err := store.Open(filepath.Join(dir, "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	l := &Library{Store: db, Media: tools}
	t.Cleanup(func() { l.Close(); db.Close() })
	tracks := []model.Track{}
	for i := 0; i < 5; i++ {
		artist, album := "Исполнитель", "Старый альбом"
		if i == 2 {
			artist = "исполнитель"
		}
		if i == 3 {
			artist = "Другой исполнитель"
		}
		if i == 4 {
			album = "Другой альбом"
		}
		path := filepath.Join(dir, fmt.Sprintf("%02d.flac", i))
		testutil.Audio(t, path, "flac", "-metadata", "title=Трек "+fmt.Sprint(i), "-metadata", "artist="+artist, "-metadata", "album="+album, "-metadata", "album_artist=Группа", "-metadata", "date=2000", "-metadata", "genre=Rock", "-metadata", "track="+fmt.Sprint(i+1), "-metadata", "disc=2", "-metadata", "composer=Автор "+fmt.Sprint(i), "-metadata", "comment=Заметка "+fmt.Sprint(i))
		track, err := tools.Probe(context.Background(), path)
		if err != nil {
			t.Fatal(err)
		}
		if err = db.Put(context.Background(), &track, path); err != nil {
			t.Fatal(err)
		}
		tracks = append(tracks, track)
	}
	return l, tracks
}
func saveAlbumChange(t *testing.T, l *Library, source model.Track, cover bool) Saved {
	t.Helper()
	tags := source.Tags
	tags.Album = "Новое название"
	tags.AlbumArtist = "Новая группа"
	tags.Date = "2026"
	tags.Genre = "Electronic"
	edit := Edit{Tags: tags, ExpectedModTime: source.ModTime, CoverAction: "keep"}
	if cover {
		edit.CoverAction = "replace"
		edit.Cover = testutil.Cover(t)
	}
	saved, err := l.Save(context.Background(), source.ID, edit)
	if err != nil {
		t.Fatal(err)
	}
	if saved.AlbumSuggestion == nil || saved.SuggestionWarning != "" {
		t.Fatalf("missing proposal: %+v", saved)
	}
	return saved
}
func waitAlbum(t *testing.T, l *Library, id string) AlbumStatus {
	t.Helper()
	done := make(chan struct{})
	go func() { l.albumWG.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("album job did not finish")
	}
	p, err := l.AlbumStatus(id)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAlbumProposalRenameSelectiveApplyAndBackups(t *testing.T) {
	l, tracks := albumFixture(t)
	original, err := os.ReadFile(tracks[1].Path)
	if err != nil {
		t.Fatal(err)
	}
	saved := saveAlbumChange(t, l, tracks[0], true)
	proposal := saved.AlbumSuggestion
	if proposal.Artist != tracks[0].Tags.Artist || proposal.Album != tracks[0].Tags.Album || len(proposal.Targets) != 2 || len(proposal.Changes) != 5 {
		t.Fatalf("incorrect rename group: %+v", proposal)
	}
	unchanged, _ := os.ReadFile(tracks[1].Path)
	if !bytes.Equal(unchanged, original) {
		t.Fatal("proposal wrote to another track without confirmation")
	}
	for _, request := range []AlbumApply{
		{Fields: []string{"album"}, TrackIDs: []int64{tracks[1].ID}},
		{Confirm: true, Fields: []string{"title"}, TrackIDs: []int64{tracks[1].ID}},
		{Confirm: true, Fields: []string{"album"}, TrackIDs: []int64{tracks[3].ID}},
	} {
		if _, err = l.StartAlbumApply(proposal.ID, request); err == nil {
			t.Fatal("unreviewed changes were accepted")
		}
	}
	job, err := l.StartAlbumApply(proposal.ID, AlbumApply{Confirm: true, Fields: []string{"album", "cover"}, TrackIDs: []int64{tracks[1].ID}})
	if err != nil {
		t.Fatal(err)
	}
	result := waitAlbum(t, l, job.ID)
	if result.Running || result.Saved != 1 || result.Failed != 0 || result.Completed != 1 {
		t.Fatalf("batch: %+v", result)
	}
	got, err := l.Media.Probe(context.Background(), tracks[1].Path)
	if err != nil {
		t.Fatal(err)
	}
	want := tracks[1].Tags
	want.Album = saved.Track.Tags.Album
	if got.Tags != want || !bytes.Equal(got.Cover, saved.Track.Cover) {
		t.Fatalf("track-specific/unselected metadata overwritten: %+v", got.Tags)
	}
	backup, err := os.ReadFile(result.Results[0].Backup)
	if err != nil || !bytes.Equal(backup, original) {
		t.Fatal("missing original backup")
	}
	dbTrack, err := l.Store.Get(tracks[1].ID)
	if err != nil || dbTrack.Tags != got.Tags || !dbTrack.HasCover {
		t.Fatal("SQLite differs from saved file")
	}
	for _, track := range tracks[2:] {
		got, err := l.Store.Get(track.ID)
		if err != nil || got.Tags != track.Tags || got.HasCover {
			t.Fatalf("unselected track changed: %+v", got)
		}
	}
	if _, err = l.StartAlbumApply(proposal.ID, AlbumApply{Confirm: true, Fields: []string{"album"}, TrackIDs: []int64{tracks[1].ID}}); err == nil {
		t.Fatal("proposal replayed")
	}
}

func TestAlbumApplyReportsFailureAndContinues(t *testing.T) {
	l, tracks := albumFixture(t)
	saved := saveAlbumChange(t, l, tracks[0], false)
	if err := os.Remove(tracks[1].Path); err != nil {
		t.Fatal(err)
	}
	job, err := l.StartAlbumApply(saved.AlbumSuggestion.ID, AlbumApply{Confirm: true, Fields: []string{"date"}, TrackIDs: []int64{tracks[1].ID, tracks[2].ID}})
	if err != nil {
		t.Fatal(err)
	}
	p := waitAlbum(t, l, job.ID)
	if p.Completed != 2 || p.Failed != 1 || p.Saved != 1 || !albumResultFailed(p, tracks[1].ID) {
		t.Fatalf("partial failures hidden: %+v", p)
	}
	got, err := l.Store.Get(tracks[2].ID)
	if err != nil || got.Tags.Date != "2026" || got.Tags.Album != tracks[2].Tags.Album {
		t.Fatal("remaining track was not updated selectively")
	}
}

func TestAlbumApplyRejectsStaleSourceAndSkipsChangedTarget(t *testing.T) {
	l, tracks := albumFixture(t)
	saved := saveAlbumChange(t, l, tracks[0], false)
	peer := tracks[1]
	peer.Tags.Title = "Изменено отдельно"
	if _, err := l.Save(context.Background(), peer.ID, Edit{Tags: peer.Tags, ExpectedModTime: peer.ModTime, CoverAction: "keep"}); err != nil {
		t.Fatal(err)
	}
	job, err := l.StartAlbumApply(saved.AlbumSuggestion.ID, AlbumApply{Confirm: true, Fields: []string{"genre"}, TrackIDs: []int64{peer.ID, tracks[2].ID}})
	if err != nil {
		t.Fatal(err)
	}
	p := waitAlbum(t, l, job.ID)
	if p.Failed != 1 || p.Saved != 1 {
		t.Fatalf("stale target was overwritten: %+v", p)
	}
	got, err := l.Store.Get(peer.ID)
	if err != nil || got.Tags != peer.Tags {
		t.Fatal("independent edit was overwritten")
	}
	// A second suggestion becomes invalid if its source changes before confirmation.
	fresh, err := l.Store.Get(tracks[2].ID)
	if err != nil {
		t.Fatal(err)
	}
	second := saveAlbumChange(t, l, fresh, false)
	changed := second.Track
	changed.Tags.Title = "Другой заголовок"
	if _, err = l.Save(context.Background(), changed.ID, Edit{Tags: changed.Tags, ExpectedModTime: changed.ModTime, CoverAction: "keep"}); err != nil {
		t.Fatal(err)
	}
	if _, err = l.StartAlbumApply(second.AlbumSuggestion.ID, AlbumApply{Confirm: true, Fields: []string{"date"}, TrackIDs: []int64{peer.ID}}); err == nil {
		t.Fatal("changed source accepted")
	}
}

func TestAlbumCancellationStopsWaitingFileAndBlocksClear(t *testing.T) {
	l, tracks := albumFixture(t)
	if err := l.Store.SetSettings(store.Settings{Theme: "system", ScanWorkers: 1}); err != nil {
		t.Fatal(err)
	}
	saved := saveAlbumChange(t, l, tracks[0], false)
	release, err := l.files.acquire(context.Background(), tracks[1].Path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	job, err := l.StartAlbumApply(saved.AlbumSuggestion.ID, AlbumApply{Confirm: true, Fields: []string{"date"}, TrackIDs: []int64{tracks[1].ID, tracks[2].ID}})
	if err != nil {
		t.Fatal(err)
	}
	if err = l.Clear(context.Background()); err == nil {
		t.Fatal("cleared library during album write")
	}
	if err = l.CancelAlbum(job.ID); err != nil {
		t.Fatal(err)
	}
	p := waitAlbum(t, l, job.ID)
	if !p.Cancelled || p.Running || p.Saved != 0 {
		t.Fatalf("cancellation failed: %+v", p)
	}
	for _, old := range tracks[1:3] {
		got, err := l.Store.Get(old.ID)
		if err != nil || got.Tags != old.Tags {
			t.Fatal("queued file changed after cancellation")
		}
	}
}

func TestTrackSpecificEditsDoNotSuggestAlbumChanges(t *testing.T) {
	l, tracks := albumFixture(t)
	track := tracks[0]
	track.Tags.Title = "Новое название трека"
	track.Tags.Track = "12"
	track.Tags.Comment = "Только этот трек"
	saved, err := l.Save(context.Background(), track.ID, Edit{Tags: track.Tags, ExpectedModTime: track.ModTime, CoverAction: "keep"})
	if err != nil || saved.AlbumSuggestion != nil {
		t.Fatalf("track-specific change suggested for album: %+v %v", saved.AlbumSuggestion, err)
	}
}

func TestAlbumCoverRemovalAndIdenticalCoverDoesNotPrompt(t *testing.T) {
	l, tracks := albumFixture(t)
	art := testutil.Cover(t)
	source, err := l.Save(context.Background(), tracks[0].ID, Edit{Tags: tracks[0].Tags, ExpectedModTime: tracks[0].ModTime, CoverAction: "replace", Cover: art})
	if err != nil || source.AlbumSuggestion == nil {
		t.Fatalf("cover proposal: %v", err)
	}
	job, err := l.StartAlbumApply(source.AlbumSuggestion.ID, AlbumApply{Confirm: true, Fields: []string{"cover"}, TrackIDs: []int64{tracks[1].ID, tracks[2].ID}})
	if err != nil {
		t.Fatal(err)
	}
	if p := waitAlbum(t, l, job.ID); p.Saved != 2 || p.Failed != 0 {
		t.Fatalf("cover apply: %+v", p)
	}
	identical, err := l.Save(context.Background(), source.Track.ID, Edit{Tags: source.Track.Tags, ExpectedModTime: source.Track.ModTime, CoverAction: "replace", Cover: art})
	if err != nil || identical.AlbumSuggestion != nil {
		t.Fatalf("identical cover prompted: %v", err)
	}
	removed, err := l.Save(context.Background(), identical.Track.ID, Edit{Tags: identical.Track.Tags, ExpectedModTime: identical.Track.ModTime, CoverAction: "remove"})
	if err != nil || removed.AlbumSuggestion == nil {
		t.Fatalf("cover removal proposal: %v", err)
	}
	job, err = l.StartAlbumApply(removed.AlbumSuggestion.ID, AlbumApply{Confirm: true, Fields: []string{"cover"}, TrackIDs: []int64{tracks[1].ID}})
	if err != nil {
		t.Fatal(err)
	}
	if p := waitAlbum(t, l, job.ID); p.Saved != 1 || p.Failed != 0 {
		t.Fatalf("cover removal: %+v", p)
	}
	for i := 1; i <= 2; i++ {
		got, err := l.Media.Probe(context.Background(), tracks[i].Path)
		if err != nil || got.HasCover != (i == 2) || got.Tags != tracks[i].Tags {
			t.Fatalf("cover removal damaged another field/file: %+v %v", got, err)
		}
	}
}

func albumResultFailed(p AlbumStatus, id int64) bool {
	for _, result := range p.Results {
		if result.ID == id {
			return result.State == "failed" && result.Error != ""
		}
	}
	return false
}
