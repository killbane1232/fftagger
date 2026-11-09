package testutil

import (
	"bytes"
	"fftagger/internal/media"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os/exec"
	"testing"
)

func Tools(t testing.TB) media.Tools {
	t.Helper()
	tools := media.Tools{FFmpeg: media.Find("ffmpeg"), FFprobe: media.Find("ffprobe")}
	if !tools.Available() {
		t.Skip("FFmpeg is required for integration tests")
	}
	return tools
}

func JPEGCover(t testing.TB) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 24, 24))
	for y := 0; y < 24; y++ {
		for x := 0; x < 24; x++ {
			img.Set(x, y, color.RGBA{160, uint8(x * 10), uint8(y * 10), 255})
		}
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func Audio(t testing.TB, path, codec string, extra ...string) {
	t.Helper()
	tools := Tools(t)
	args := []string{"-v", "error", "-y", "-f", "lavfi", "-i", "sine=frequency=440:duration=0.4", "-ac", "2", "-c:a", codec, "-strict", "-2", "-metadata", "title=Исходный трек", "-metadata", "artist=Исполнитель", "-metadata", "album=Альбом"}
	args = append(args, extra...)
	args = append(args, path)
	if out, err := exec.Command(tools.FFmpeg, args...).CombinedOutput(); err != nil {
		t.Fatalf("create audio: %v: %s", err, out)
	}
}
func Cover(t testing.TB) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 16, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 16; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 16), uint8(y * 16), 125, 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
