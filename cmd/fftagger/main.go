package main

import (
	"context"
	"fftagger/internal/desktop"
	"fftagger/internal/library"
	"fftagger/internal/media"
	"fftagger/internal/metadata"
	"fftagger/internal/server"
	"fftagger/internal/store"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

func main() {
	runtime.LockOSThread()
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	config, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	dbPath := flag.String("db", filepath.Join(config, "FFTagger", "library.sqlite"), "SQLite library path")
	headless := flag.Bool("headless", false, "serve in an external browser without opening a native window")
	address := flag.String("listen", "127.0.0.1:0", "loopback listen address")
	ffmpeg := flag.String("ffmpeg", media.Find("ffmpeg"), "path to ffmpeg")
	ffprobe := flag.String("ffprobe", media.Find("ffprobe"), "path to ffprobe")
	scanWorkers := flag.Int("scan-workers", 0, "save scan worker setting: 0 = automatic (2–8), or 1–16; omitted = saved setting")
	ua := flag.String("musicbrainz-agent", "FFTagger/0.1 (local macOS music library)", "MusicBrainz User-Agent; supply a contact URL/email for distribution")
	flag.Parse()
	*dbPath, err = filepath.Abs(*dbPath)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(*dbPath), 0700); err != nil {
		return err
	}
	// Reject a second writer to the same library before opening SQLite.
	lock, err := os.OpenFile(*dbPath+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return fmt.Errorf("библиотека уже открыта в другом экземпляре FFTagger: %w", err)
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	db, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	workersProvided := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "scan-workers" {
			workersProvided = true
		}
	})
	if workersProvided {
		if _, err = db.UpdateSettings(store.SettingsPatch{ScanWorkers: scanWorkers}); err != nil {
			return err
		}
	}
	lib := &library.Library{Store: db, Media: media.Tools{FFmpeg: *ffmpeg, FFprobe: *ffprobe}, DBPath: *dbPath}
	defer lib.Close()
	host, _, err := net.SplitHostPort(*address)
	if err != nil {
		return err
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("listen must use a numeric loopback address")
	}
	listener, err := net.Listen("tcp", *address)
	if err != nil {
		return err
	}
	defer listener.Close()
	picker := desktop.ChooseFolder
	if *headless {
		picker = func() (string, error) {
			return "", fmt.Errorf("в режиме браузера введите путь к папке вручную")
		}
	}
	app, err := server.New(lib, metadata.New(*ua), listener.Addr().String(), picker)
	if err != nil {
		return err
	}
	httpServer := &http.Server{Handler: app.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	if !*headless && runtime.GOOS == "darwin" {
		app.SetTheme = desktop.SetTheme
		settings, err := db.Settings()
		if err != nil {
			return err
		}
		desktop.SetTheme(settings.Theme)
	}
	go func() {
		if err := httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Printf("HTTP: %v", err)
		}
	}()
	fmt.Println("FFTagger:", app.URL())
	if *headless || runtime.GOOS != "darwin" {
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		<-ctx.Done()
	} else {
		desktop.Run(app.URL())
	}
	lib.Cancel()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return httpServer.Shutdown(ctx)
}
