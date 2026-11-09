package library

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fftagger/internal/media"
	"fftagger/internal/model"
	"fftagger/internal/retry"
	"fftagger/internal/store"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Progress struct {
	Running     bool     `json:"running"`
	Root        string   `json:"root"`
	Current     string   `json:"current"`
	Visited     int      `json:"visited"`
	Imported    int      `json:"imported"`
	Unchanged   int      `json:"unchanged"`
	Skipped     int      `json:"skipped"`
	Errors      []string `json:"errors"`
	Cancelled   bool     `json:"cancelled"`
	Workers     int      `json:"workers"`
	Active      int      `json:"active"`
	ActiveFiles []string `json:"active_files"`
}
type Library struct {
	Store       *store.Store
	Media       media.Tools
	DBPath      string
	files       fileLocks
	mu          sync.Mutex
	progress    Progress
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	maintenance sync.RWMutex
	albumMu     sync.Mutex
	albumPlans  map[string]*albumPlan
	albumJob    *albumJob
	albumWG     sync.WaitGroup
}

func (l *Library) Status() Progress {
	l.mu.Lock()
	defer l.mu.Unlock()
	p := l.progress
	p.Errors = append([]string{}, p.Errors...)
	p.ActiveFiles = append([]string{}, p.ActiveFiles...)
	return p
}
func (l *Library) Cancel() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.cancel != nil {
		l.cancel()
	}
}
func (l *Library) Close() { l.Cancel(); l.cancelAlbum(); l.wg.Wait(); l.albumWG.Wait() }
func (l *Library) Start(root string) error {
	if !l.maintenance.TryRLock() {
		return errors.New("библиотека очищается; дождитесь завершения")
	}
	defer l.maintenance.RUnlock()
	if !l.Media.Available() {
		return errors.New("установите FFmpeg: brew install ffmpeg")
	}
	settings, err := l.Store.Settings()
	if err != nil {
		return err
	}
	workers, err := workerCount(settings.ScanWorkers)
	if err != nil {
		return err
	}
	root, err = resolveFolder(root)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.progress.Running {
		return errors.New("сканирование уже выполняется")
	}
	if _, err = l.Store.RememberSource(root, true); err != nil {
		return fmt.Errorf("сохранение источника: %w", err)
	}
	if err = l.Store.ClearSkipped(root); err != nil {
		return fmt.Errorf("подготовка отчёта сканирования: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	l.cancel = cancel
	l.progress = Progress{Running: true, Root: root, Errors: []string{}, Workers: workers, ActiveFiles: []string{}}
	l.wg.Add(1)
	go func() {
		defer l.wg.Done()
		l.scan(ctx, root, workers, l.Media.Probe)
		cancelled := ctx.Err() != nil
		cancel()
		l.mu.Lock()
		l.progress.Running = false
		l.progress.Current = ""
		l.progress.Active = 0
		l.progress.ActiveFiles = nil
		l.progress.Cancelled = cancelled
		l.cancel = nil
		l.mu.Unlock()
	}()
	return nil
}
func (l *Library) change(fn func(*Progress)) { l.mu.Lock(); defer l.mu.Unlock(); fn(&l.progress) }
func (l *Library) report(path string, err error) {
	l.change(func(p *Progress) {
		if len(p.Errors) < 100 {
			p.Errors = append(p.Errors, path+": "+err.Error())
		}
	})
}
func workerCount(requested int) (int, error) {
	if requested < 0 || requested > 16 {
		return 0, errors.New("число потоков сканирования должно быть от 1 до 16, либо 0 для автоматического выбора")
	}
	if requested != 0 {
		return requested, nil
	}
	n := runtime.GOMAXPROCS(0)
	if n < 2 {
		n = 2
	}
	if n > 8 {
		n = 8
	}
	return n, nil
}

type probeFile func(context.Context, string) (model.Track, error)

func (l *Library) scan(ctx context.Context, root string, workers int, probe probeFile) {
	skip := func(path, kind, reason, detail string) {
		if ctx.Err() != nil {
			return
		}
		l.change(func(p *Progress) { p.Skipped++ })
		if err := l.Store.RecordSkippedContext(ctx, root, path, kind, reason, detail); err != nil && ctx.Err() == nil {
			l.report(path, fmt.Errorf("сохранение причины пропуска: %w", err))
		}
	}
	resolve := func(path string) {
		if err := l.Store.ResolveSkippedContext(ctx, path); err != nil && ctx.Err() == nil {
			l.report(path, err)
		}
	}
	// Only paths are queued: no unbounded goroutines, audio buffers or artwork
	// waiting in memory. Each worker overlaps probing with SQLite's one writer.
	jobs := make(chan string, workers*2)
	var pool sync.WaitGroup
	process := func(path string) {
		release, err := l.files.acquire(ctx, path)
		if err != nil {
			return
		}
		defer release()
		if ctx.Err() != nil {
			return
		}
		l.change(func(p *Progress) {
			p.Visited++
			p.ActiveFiles = append(p.ActiveFiles, path)
			p.Active = len(p.ActiveFiles)
			p.Current = path
		})
		defer l.change(func(p *Progress) {
			for i, v := range p.ActiveFiles {
				if v == path {
					p.ActiveFiles = append(p.ActiveFiles[:i], p.ActiveFiles[i+1:]...)
					break
				}
			}
			p.Active = len(p.ActiveFiles)
			p.Current = ""
			if p.Active > 0 {
				p.Current = p.ActiveFiles[0]
			}
		})
		// Stat after acquiring the file lock: an editor may have saved it while
		// it was queued. Never import the earlier snapshot over the edit.
		info, err := os.Lstat(path)
		if err != nil {
			// A queued scan may reach a file just deleted through the editor.
			// Do not recreate a skipped-file report for an absent catalog entry.
			if errors.Is(err, os.ErrNotExist) {
				if _, lookup := l.Store.ByPath(path); errors.Is(lookup, store.ErrNotFound) {
					resolve(path)
					return
				}
			}
			skip(path, "read_error", "Не удалось прочитать файл", err.Error())
			l.report(path, err)
			return
		}
		if !info.Mode().IsRegular() {
			skip(path, "excluded", "Необычный тип файла", "Импортируются только обычные файлы.")
			return
		}
		old, err := l.Store.ByPath(path)
		if err == nil && old.Size == info.Size() && old.ModTime == info.ModTime().UnixNano() {
			resolve(path)
			l.change(func(p *Progress) { p.Unchanged++ })
			return
		}
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			skip(path, "import_error", "Не удалось прочитать библиотеку", err.Error())
			l.report(path, err)
			return
		}
		tr, err := probe(ctx, path)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			kind, reason := "not_audio", "FFprobe не распознал аудио"
			if likelyAudio(path) && !errors.Is(err, media.ErrNotAudio) {
				kind = "read_error"
				reason = "Не удалось прочитать аудиофайл"
			}
			skip(path, kind, reason, err.Error())
			if kind == "read_error" {
				l.report(path, err)
			}
			return
		}
		if err = l.Store.Put(ctx, &tr, path); err != nil {
			if ctx.Err() != nil {
				return
			}
			l.report(path, err)
			skip(path, "import_error", "Не удалось сохранить в библиотеку", err.Error())
			return
		}
		l.change(func(p *Progress) { p.Imported++ })
	}
	for i := 0; i < workers; i++ {
		pool.Add(1)
		go func() {
			defer pool.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case path, ok := <-jobs:
					if !ok || ctx.Err() != nil {
						return
					}
					process(path)
				}
			}
		}()
	}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walkErr != nil {
			skip(path, "read_error", "Не удалось прочитать файл или папку", walkErr.Error())
			l.report(path, walkErr)
			return nil
		}
		if d.IsDir() {
			if path != root && (d.Name() == ".fftagger-backups" || strings.HasPrefix(d.Name(), ".fftagger-delete-")) {
				return filepath.SkipDir
			}
			if path != root && (strings.HasPrefix(d.Name(), ".") || strings.HasSuffix(d.Name(), ".app")) {
				skip(path, "excluded", "Папка исключена из обхода", "Скрытые папки и пакеты .app не сканируются. Их содержимое не перечисляется.")
				return filepath.SkipDir
			}
			return nil
		}
		if path == l.DBPath || path == l.DBPath+".lock" || strings.HasPrefix(path, l.DBPath+"-") || strings.HasPrefix(d.Name(), ".fftagger-") {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			skip(path, "excluded", "Символическая ссылка", "Ссылки не обходятся, чтобы не импортировать дубликаты и не выходить за пределы папки.")
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			skip(path, "excluded", "Скрытый файл", "Скрытые файлы не сканируются.")
			return nil
		}
		if !d.Type().IsRegular() {
			skip(path, "excluded", "Необычный тип файла", "Импортируются только обычные файлы.")
			return nil
		}
		select {
		case jobs <- path:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	close(jobs)
	pool.Wait()
	if err != nil && !errors.Is(err, context.Canceled) {
		l.report(root, err)
	}
}
func likelyAudio(path string) bool {
	return strings.Contains("|mp3|flac|m4a|m4b|aac|ogg|opus|wav|aiff|aif|wma|ape|wv|dsf|dff|alac|ac3|dts|amr|au|caf|", "|"+strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))+"|")
}

type Edit struct {
	Tags            model.Tags `json:"tags"`
	ExpectedModTime int64      `json:"expected_mod_time,string"`
	CoverAction     string     `json:"cover_action"`
	Cover           []byte     `json:"cover"`
}
type Saved struct {
	Track             model.Track      `json:"track"`
	Backup            string           `json:"backup"`
	AlbumSuggestion   *AlbumSuggestion `json:"album_suggestion,omitempty"`
	SuggestionWarning string           `json:"suggestion_warning,omitempty"`
}

func (l *Library) Save(ctx context.Context, id int64, e Edit) (Saved, error) {
	return l.saveTrack(ctx, id, e, true)
}

func (l *Library) saveTrack(ctx context.Context, id int64, e Edit, suggest bool) (Saved, error) {
	if !l.maintenance.TryRLock() {
		return Saved{}, errors.New("библиотека очищается; дождитесь завершения")
	}
	defer l.maintenance.RUnlock()
	old, err := l.Store.Get(id)
	if err != nil {
		return Saved{}, err
	}
	release, err := l.files.acquire(ctx, old.Path)
	if err != nil {
		return Saved{}, err
	}
	defer release()
	old, err = l.Store.Get(id)
	if err != nil {
		return Saved{}, err
	}
	old.Cover, err = l.Store.Cover(id)
	if err != nil {
		return Saved{}, err
	}
	if e.CoverAction != "keep" && e.CoverAction != "replace" && e.CoverAction != "remove" {
		return Saved{}, errors.New("неизвестное действие с обложкой")
	}
	if e.CoverAction == "replace" {
		if err = media.ValidateCover(e.Cover); err != nil {
			return Saved{}, err
		}
	} else {
		e.Cover = nil
	}
	var updated model.Track
	var backup string
	var original os.FileInfo
	err = retry.Do(ctx, func() error {
		info, err := os.Lstat(old.Path)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return retry.Permanent(errors.New("исходный путь больше не является обычным файлом"))
		}
		if old.ModTime != e.ExpectedModTime || info.Size() != old.Size || info.ModTime().UnixNano() != old.ModTime || (original != nil && !os.SameFile(original, info)) {
			return retry.Permanent(errors.New("файл изменён другим приложением; пересканируйте папку и откройте трек заново"))
		}
		original = info
		updated, backup, err = l.writeReplacement(ctx, old, e, info)
		return err
	})
	if err != nil {
		return Saved{}, err
	}
	// Complete or roll back even if the HTTP client disconnects after replacement.
	if err = l.Store.Put(context.Background(), &updated, old.Path); err != nil {
		if rollback := retry.Do(context.Background(), func() error { return os.Rename(backup, old.Path) }); rollback != nil {
			return Saved{}, fmt.Errorf("БД: %v; восстановление: %v; оригинал сохранён в %s", err, rollback, backup)
		}
		return Saved{}, fmt.Errorf("ошибка БД; оригинал восстановлен: %w", err)
	}
	result := Saved{Track: updated, Backup: backup}
	if suggest {
		result.AlbumSuggestion, err = l.suggestAlbum(old, updated)
		if err != nil {
			result.SuggestionWarning = "Трек сохранён, но не удалось подготовить изменения для альбома: " + err.Error()
		}
	}
	return result, nil
}

// writeReplacement may be retried only before a confirmed replacement. Every
// attempt uses its own temporary files and verifies the unchanged original.
func (l *Library) writeReplacement(ctx context.Context, old model.Track, e Edit, info os.FileInfo) (model.Track, string, error) {
	updated, temp, err := l.Media.Rewrite(ctx, old, e.Tags, e.Cover, e.CoverAction != "keep")
	if err != nil {
		return model.Track{}, "", err
	}
	defer os.Remove(temp)
	fd, err := os.OpenFile(temp, os.O_RDWR, 0)
	if err != nil {
		return model.Track{}, "", err
	}
	// Open while the temporary file is still writable. The original may have
	// read-only mode bits, which must not prevent syncing the replacement.
	err = fd.Chmod(info.Mode().Perm())
	if err == nil {
		err = fd.Sync()
	}
	closeErr := fd.Close()
	if err != nil {
		return model.Track{}, "", err
	}
	if closeErr != nil {
		return model.Track{}, "", closeErr
	}
	if err = unchangedFile(old.Path, info); err != nil {
		return model.Track{}, "", err
	}
	if err = ctx.Err(); err != nil {
		return model.Track{}, "", err
	}
	dir := filepath.Join(filepath.Dir(old.Path), ".fftagger-backups")
	if err = os.MkdirAll(dir, 0700); err != nil {
		return model.Track{}, "", err
	}
	var nonce [8]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return model.Track{}, "", err
	}
	backup := filepath.Join(dir, old.Name+"."+time.Now().Format("20060102-150405")+"."+hex.EncodeToString(nonce[:])+".bak")
	// Prefer a hard link; SMB/exFAT may require a streamed backup copy instead.
	if err = backupOriginal(ctx, old.Path, backup, info); err != nil {
		return model.Track{}, "", fmt.Errorf("резервная копия: %w", err)
	}
	keepBackup := false
	defer func() {
		if !keepBackup {
			os.Remove(backup)
		}
	}()
	// A network backup can take time. Check again immediately before replacing
	// the source so changes made during the copy are not overwritten.
	if err = unchangedFile(old.Path, info); err != nil {
		return model.Track{}, "", err
	}
	if err = ctx.Err(); err != nil {
		return model.Track{}, "", err
	}
	if err = os.Rename(temp, old.Path); err != nil {
		// A server may have committed a rename before the connection failed.
		// Never retry or discard the backup unless the original is still intact.
		if check := unchangedFile(old.Path, info); check != nil {
			keepBackup = true
			return model.Track{}, "", retry.Permanent(fmt.Errorf("не удалось подтвердить замену файла: %v; проверка исходника: %v; резервная копия: %s", err, check, backup))
		}
		return model.Track{}, "", err
	}
	keepBackup = true
	return updated, backup, nil
}

func (l *Library) Clear(ctx context.Context) error {
	if !l.maintenance.TryLock() {
		return errors.New("дождитесь завершения сохранения или текущей очистки библиотеки")
	}
	defer l.maintenance.Unlock()
	l.Cancel()
	l.wg.Wait()
	if err := l.Store.Clear(ctx); err != nil {
		return err
	}
	l.change(func(p *Progress) { *p = Progress{Errors: []string{}, ActiveFiles: []string{}} })
	return nil
}
