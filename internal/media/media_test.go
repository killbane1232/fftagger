package media_test

import (
	"bytes"
	"context"
	"fftagger/internal/model"
	"fftagger/internal/testutil"
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTripTagsAndArtwork(t *testing.T) {
	tools := testutil.Tools(t)
	ctx := context.Background()
	for _, tc := range []struct {
		ext, codec string
		art        bool
	}{{"mp3", "libmp3lame", true}, {"flac", "flac", true}, {"m4a", "aac", true}, {"opus", "libopus", true}, {"ogg", "vorbis", true}, {"aiff", "pcm_s16be", true}, {"wav", "pcm_s16le", false}, {"wv", "wavpack", false}, {"wma", "wmav2", false}} {
		t.Run(tc.ext, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "audio."+tc.ext)
			var extra []string
			custom := "literal \\;#=\ntrailing\\"
			if tc.ext == "ogg" || tc.ext == "opus" {
				extra = []string{"-metadata", "custom_note=" + custom}
			}
			testutil.Audio(t, path, tc.codec, extra...)
			old, err := tools.Probe(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			original, err := tools.AudioHash(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			tags := old.Tags
			tags.Title = "Новый трек № 1"
			tags.Artist = "Ёлка"
			tags.Album = "Весна"
			tags.Comment = "Первая строка\nВторая = # ; \\"
			if tc.art {
				tags.AlbumArtist = "Разные исполнители"
				tags.Date = "2024"
				tags.Track = "2/10"
				tags.Disc = "1/2"
				tags.Genre = "Electronic"
				tags.Composer = "Автор"
			}
			cover := testutil.Cover(t)
			if !tc.art {
				cover = nil
			}
			out, temp, err := tools.Rewrite(ctx, old, tags, cover, tc.art)
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(temp)
			if out.Tags.Artist != tags.Artist || !bytes.Equal(out.Cover, cover) {
				t.Fatalf("tags/cover mismatch: %+v", out)
			}
			if err = os.Rename(temp, path); err != nil {
				t.Fatal(err)
			}
			// Changing only tags must retain the exact embedded cover.
			out.Tags.Artist = "Другой исполнитель"
			out2, temp2, err := tools.Rewrite(ctx, out, out.Tags, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(temp2)
			if err = os.Rename(temp2, path); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(out2.Cover, cover) {
				t.Fatal("cover lost on metadata-only update")
			}
			if tc.art {
				replacement := testutil.JPEGCover(t)
				replaced, temp4, e := tools.Rewrite(ctx, out2, out2.Tags, replacement, true)
				if e != nil {
					t.Fatal(e)
				}
				defer os.Remove(temp4)
				if !bytes.Equal(replaced.Cover, replacement) {
					t.Fatal("existing artwork was not replaced")
				}
				if e = os.Rename(temp4, path); e != nil {
					t.Fatal(e)
				}
				out2 = replaced
			}
			// Deleting all supported fields and artwork must really remove them.
			out3, temp3, err := tools.Rewrite(ctx, out2, model.Tags{}, nil, tc.art)
			if err != nil {
				t.Fatal(err)
			}
			defer os.Remove(temp3)
			if out3.Tags != (model.Tags{}) || out3.HasCover {
				t.Fatalf("deletion failed: %+v", out3)
			}
			if len(extra) > 0 && out3.RawTags["custom_note"] != custom {
				t.Fatalf("custom tag changed: %q", out3.RawTags["custom_note"])
			}
			got, err := tools.AudioHash(ctx, temp3)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(original, got) {
				t.Fatal("audio packets changed")
			}
		})
	}
}

func TestRejectCoverAndUnsupportedMetadataWithoutChangingOriginal(t *testing.T) {
	tools := testutil.Tools(t)
	path := filepath.Join(t.TempDir(), "audio.wav")
	testutil.Audio(t, path, "pcm_s16le")
	before, _ := os.ReadFile(path)
	tr, err := tools.Probe(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = tools.Rewrite(context.Background(), tr, tr.Tags, testutil.Cover(t), true); err == nil {
		t.Fatal("WAV artwork must fail")
	}
	tags := tr.Tags
	tags.AlbumArtist = "Unsupported"
	if _, _, err = tools.Rewrite(context.Background(), tr, tags, nil, false); err == nil {
		t.Fatal("unsupported tag must not silently disappear")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("original changed")
	}
}
