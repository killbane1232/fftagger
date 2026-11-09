package media

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fftagger/internal/model"
	"fftagger/internal/retry"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

var ErrNotAudio = errors.New("нет аудиопотока")

type Tools struct{ FFmpeg, FFprobe string }

func Find(name string) string {
	if p, e := exec.LookPath(name); e == nil {
		return p
	}
	exe, _ := os.Executable()
	for _, dir := range []string{filepath.Join(filepath.Dir(exe), "../Resources/bin"), "/opt/homebrew/bin", "/usr/local/bin"} {
		p := filepath.Join(dir, name)
		if f, e := os.Stat(p); e == nil && !f.IsDir() {
			return p
		}
	}
	return ""
}
func (t Tools) Available() bool { return t.FFmpeg != "" && t.FFprobe != "" }

type stream struct {
	Index       int               `json:"index"`
	CodecType   string            `json:"codec_type"`
	CodecName   string            `json:"codec_name"`
	SampleRate  string            `json:"sample_rate"`
	Channels    int               `json:"channels"`
	Duration    string            `json:"duration"`
	Tags        map[string]string `json:"tags"`
	Disposition struct {
		Attached int `json:"attached_pic"`
	} `json:"disposition"`
}
type probe struct {
	Streams []stream `json:"streams"`
	Format  struct {
		Name     string            `json:"format_name"`
		Duration string            `json:"duration"`
		Tags     map[string]string `json:"tags"`
	} `json:"format"`
}
type limitedBuffer struct {
	bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.max {
		return 0, errors.New("слишком большой ответ FFmpeg")
	}
	return b.Buffer.Write(p)
}
func command(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	out := &limitedBuffer{max: 24 << 20}
	stderr := &limitedBuffer{max: 64 << 10}
	cmd.Stdout = out
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%s: %w: %s", filepath.Base(bin), err, strings.TrimSpace(stderr.String()))
	}
	return out.Bytes(), nil
}
func (t Tools) Probe(ctx context.Context, path string) (model.Track, error) {
	if !t.Available() {
		return model.Track{}, errors.New("установите FFmpeg и ffprobe: brew install ffmpeg")
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	// Opening first refreshes metadata on network filesystems. A just-written
	// SMB file may otherwise report a cached timestamp until its first open.
	source, err := os.Open(path)
	if err != nil {
		return model.Track{}, err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return model.Track{}, err
	}
	data, err := command(ctx, t.FFprobe, "-v", "error", "-protocol_whitelist", "file,pipe", "-show_format", "-show_streams", "-of", "json", path)
	if err != nil {
		return model.Track{}, err
	}
	var p probe
	if err = json.Unmarshal(data, &p); err != nil {
		return model.Track{}, err
	}
	var audio *stream
	coverIndex := -1
	coverCodec := ""
	for i := range p.Streams {
		s := &p.Streams[i]
		if s.CodecType == "audio" && audio == nil {
			audio = s
		}
		if s.Disposition.Attached == 1 && coverIndex < 0 {
			coverIndex = s.Index
			coverCodec = s.CodecName
		}
	}
	if audio == nil {
		return model.Track{}, ErrNotAudio
	}
	raw := map[string]string{}
	for _, tags := range []map[string]string{p.Format.Tags, audio.Tags} {
		for k, v := range tags {
			raw[strings.ToLower(k)] = v
		}
	}
	first := func(keys ...string) string {
		for _, k := range keys {
			if v := raw[k]; v != "" {
				return v
			}
		}
		return ""
	}
	tr := model.Track{Path: path, Name: filepath.Base(path), Size: info.Size(), ModTime: info.ModTime().UnixNano(), Format: p.Format.Name, Codec: audio.CodecName, Channels: audio.Channels, RawTags: raw}
	tr.Tags = model.Tags{Title: first("title"), Artist: first("artist", "author"), Album: first("album"), AlbumArtist: first("album_artist", "albumartist"), Date: first("date", "year"), Track: first("track", "tracknumber"), Disc: first("disc", "discnumber"), Genre: first("genre"), Composer: first("composer"), Comment: first("comment", "description")}
	tr.Duration, _ = strconv.ParseFloat(p.Format.Duration, 64)
	if tr.Duration == 0 {
		tr.Duration, _ = strconv.ParseFloat(audio.Duration, 64)
	}
	tr.SampleRate, _ = strconv.Atoi(audio.SampleRate)
	_, tr.Writable, tr.CoverWritable = container(p.Format.Name)
	for _, s := range p.Streams {
		if s.CodecType == "video" && s.Disposition.Attached != 1 {
			tr.Writable = false
			tr.CoverWritable = false
		}
	}
	if coverIndex >= 0 {
		codec := "copy"
		if coverCodec != "mjpeg" && coverCodec != "png" {
			codec = "mjpeg"
		}
		tr.Cover, err = command(ctx, t.FFmpeg, "-v", "error", "-nostdin", "-protocol_whitelist", "file,pipe", "-i", path, "-map", fmt.Sprintf("0:%d", coverIndex), "-frames:v", "1", "-c:v", codec, "-f", "image2pipe", "pipe:1")
		if err != nil {
			return tr, fmt.Errorf("извлечение обложки: %w", err)
		}
		if _, _, e := image.DecodeConfig(bytes.NewReader(tr.Cover)); e != nil {
			return tr, fmt.Errorf("повреждённая обложка: %w", e)
		}
	}
	tr.HasCover = len(tr.Cover) > 0
	return tr, nil
}
func container(format string) (muxer string, writable, cover bool) {
	switch strings.Split(format, ",")[0] {
	case "mp3":
		return "mp3", true, true
	case "flac":
		return "flac", true, true
	case "mov":
		return "mp4", true, true
	case "ogg":
		return "ogg", true, true
	case "wav":
		return "wav", true, false
	case "aiff":
		return "aiff", true, true
	case "asf":
		return "asf", true, false
	case "wv":
		return "wv", true, false
	default:
		return "", false, false
	}
}

const MaxCover = 16 << 20

func ValidateCover(data []byte) error {
	if len(data) == 0 || len(data) > MaxCover {
		return errors.New("обложка должна быть JPEG или PNG размером до 16 МБ")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png") {
		return errors.New("поддерживаются только JPEG и PNG")
	}
	if cfg.Width < 1 || cfg.Height < 1 || int64(cfg.Width)*int64(cfg.Height) > 40_000_000 {
		return errors.New("обложка слишком большая: максимум 40 мегапикселей")
	}
	return nil
}
func pictureBlock(data []byte) string {
	cfg, kind, _ := image.DecodeConfig(bytes.NewReader(data))
	mime := "image/" + kind
	if kind == "jpeg" {
		mime = "image/jpeg"
	}
	var b bytes.Buffer
	put := func(n int) { binary.Write(&b, binary.BigEndian, uint32(n)) }
	put(3)
	put(len(mime))
	b.WriteString(mime)
	put(0)
	put(cfg.Width)
	put(cfg.Height)
	put(24)
	put(0)
	put(len(data))
	b.Write(data)
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

func createTempBesideTrack(dir, pattern string) (*os.File, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err == nil {
		return f, nil
	}
	if errors.Is(err, os.ErrPermission) {
		return nil, fmt.Errorf("нет права создавать файлы в папке %q; для сохранения тегов нужен доступ на запись к папке, а не только к треку. На сетевом диске проверьте права этой папки на сервере. Оригинал не изменён: %w", dir, err)
	}
	return nil, fmt.Errorf("не удалось создать временный файл в папке %q; оригинал не изменён: %w", dir, err)
}

// Rewrite produces a verified temporary file next to the original. The caller
// handles backup, atomic replacement, and the SQLite update.
func (t Tools) Rewrite(ctx context.Context, old model.Track, tags model.Tags, cover []byte, changeCover bool) (result model.Track, temp string, err error) {
	mux, writable, coverWritable := container(old.Format)
	if !old.Writable || !writable {
		return result, "", retry.Permanent(errors.New("FFmpeg не поддерживает безопасную запись тегов этого контейнера; доступно чтение"))
	}
	if changeCover && !coverWritable {
		return result, "", retry.Permanent(errors.New("этот контейнер не поддерживает запись обложки через FFmpeg"))
	}
	if changeCover && len(cover) > 0 {
		if err = ValidateCover(cover); err != nil {
			return result, "", err
		}
	}
	f, err := createTempBesideTrack(filepath.Dir(old.Path), ".fftagger-edit-*")
	if err != nil {
		return result, "", err
	}
	temp = f.Name()
	if err = f.Close(); err != nil {
		os.Remove(temp)
		return result, temp, err
	}
	defer func() {
		if err != nil {
			os.Remove(temp)
		}
	}()
	args := []string{"-v", "error", "-nostdin", "-y", "-protocol_whitelist", "file,pipe", "-i", old.Path}
	if mux == "ogg" {
		if !changeCover {
			cover = old.Cover
		}
		mf, e := createTempBesideTrack(filepath.Dir(old.Path), ".fftagger-metadata-*")
		if e != nil {
			return result, temp, e
		}
		defer os.Remove(mf.Name())
		var content strings.Builder
		content.WriteString(";FFMETADATA1\n")
		// Only put the base64 artwork in ffmetadata. Some FFmpeg versions
		// treat a text value ending in an escaped backslash as a continuation
		// and swallow the next tag. Copy other metadata directly from input 0.
		if len(cover) > 0 {
			content.WriteString("METADATA_BLOCK_PICTURE=" + pictureBlock(cover) + "\n")
		}
		_, e = mf.WriteString(content.String())
		closeErr := mf.Close()
		if e != nil {
			return result, temp, e
		}
		if closeErr != nil {
			return result, temp, closeErr
		}
		args = append(args, "-f", "ffmetadata", "-i", mf.Name())
	}
	if changeCover && len(cover) > 0 && mux != "ogg" {
		cf, e := createTempBesideTrack(filepath.Dir(old.Path), ".fftagger-cover-*")
		if e != nil {
			return result, temp, e
		}
		cp := cf.Name()
		defer os.Remove(cp)
		_, e = cf.Write(cover)
		closeErr := cf.Close()
		if e != nil {
			return result, temp, e
		}
		if closeErr != nil {
			return result, temp, closeErr
		}
		args = append(args, "-i", cp)
	}
	args = append(args, "-map", "0")
	if changeCover || mux == "ogg" {
		args = append(args, "-map", "-0:v?")
	}
	if changeCover && len(cover) > 0 && mux != "ogg" {
		args = append(args, "-map", "1:v:0")
	}
	args = append(args, "-map_metadata", "0", "-c", "copy")
	if mux == "ogg" {
		args = append(args, "-map_metadata:s:a:0", "0:s:a:0", "-map_metadata:s:a:0", "1")
		if len(cover) == 0 {
			args = append(args, "-metadata:s:a:0", "METADATA_BLOCK_PICTURE=")
		}
		args = append(args, "-metadata:s:a:0", "COVERART=", "-metadata:s:a:0", "COVERARTMIME=")
	}
	if changeCover && len(cover) > 0 && mux != "ogg" {
		args = append(args, "-disposition:v:0", "attached_pic", "-metadata:s:v:0", "title=Album cover", "-metadata:s:v:0", "comment=Cover (front)")
	}
	keys := []string{}
	for k := range tags.Map() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		value := tags.Map()[k]
		if strings.ContainsRune(value, 0) || len(value) > 16384 {
			return result, temp, retry.Permanent(errors.New("недопустимый или слишком длинный тег"))
		}
		args = append(args, "-metadata", k+"="+value)
		if mux == "ogg" {
			args = append(args, "-metadata:s:a:0", k+"="+value)
		}
	}
	if mux == "mp3" {
		args = append(args, "-id3v2_version", "3")
	}
	if mux == "mp4" {
		args = append(args, "-movflags", "+faststart")
	}
	if mux == "aiff" {
		args = append(args, "-write_id3v2", "1")
	}
	args = append(args, "-f", mux, temp)
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	if _, err = command(ctx, t.FFmpeg, args...); err != nil {
		return result, temp, err
	}
	result, err = t.Probe(ctx, temp)
	if err != nil {
		return result, temp, err
	}
	if result.Codec != old.Codec || result.Channels != old.Channels || result.SampleRate != old.SampleRate {
		return result, temp, retry.Permanent(errors.New("проверка аудиопотока после записи не пройдена"))
	}
	beforeHash, e := t.AudioHash(ctx, old.Path)
	if e != nil {
		return result, temp, e
	}
	afterHash, e := t.AudioHash(ctx, temp)
	if e != nil {
		return result, temp, e
	}
	if !bytes.Equal(beforeHash, afterHash) {
		return result, temp, retry.Permanent(errors.New("контрольная сумма аудиопотока изменилась; оригинал не перезаписан"))
	}
	for k, want := range tags.Map() {
		if got := result.Tags.Map()[k]; got != want && !equivalentNumber(k, want, got) {
			return result, temp, retry.Permanent(fmt.Errorf("контейнер не сохранил тег %s (ожидалось %q, получено %q); оригинал не изменён", k, want, got))
		}
	}
	if changeCover {
		if !bytes.Equal(result.Cover, cover) {
			return result, temp, retry.Permanent(errors.New("обложка не прошла проверку после записи; оригинал не изменён"))
		}
	} else if !bytes.Equal(result.Cover, old.Cover) {
		return result, temp, retry.Permanent(fmt.Errorf("исходная обложка не сохранилась (%d → %d байт); оригинал не изменён", len(old.Cover), len(result.Cover)))
	}
	result.Path = old.Path
	result.Name = old.Name
	result.ID = old.ID
	return result, temp, nil
}

// Hash compressed audio packets, not container bytes or tags. This also checks
// that every audio stream survives the remux without re-encoding.
func (t Tools) AudioHash(ctx context.Context, path string) ([]byte, error) {
	return command(ctx, t.FFmpeg, "-v", "error", "-nostdin", "-protocol_whitelist", "file,pipe", "-i", path, "-map", "0:a", "-c", "copy", "-f", "streamhash", "-hash", "sha256", "pipe:1")
}
func equivalentNumber(key, a, b string) bool {
	if key != "track" && key != "disc" {
		return false
	}
	if a == "" || b == "" {
		return false
	}
	aa, bb := strings.Split(a, "/"), strings.Split(b, "/")
	if len(aa) != len(bb) {
		return false
	}
	for i := range aa {
		x, e := strconv.Atoi(aa[i])
		y, f := strconv.Atoi(bb[i])
		if e != nil || f != nil || x != y {
			return false
		}
	}
	return true
}
