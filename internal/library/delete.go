package library

import (
	"context"
	"errors"
	"fftagger/internal/model"
	"fftagger/internal/retry"
	"fmt"
	"os"
	"path/filepath"
)

type DeleteRequest struct {
	Confirm         bool   `json:"confirm"`
	ExpectedPath    string `json:"expected_path"`
	ExpectedModTime int64  `json:"expected_mod_time,string"`
}

type Deleted struct {
	AlreadyMissing bool   `json:"already_missing"`
	Warning        string `json:"warning,omitempty"`
}

func (l *Library) Delete(ctx context.Context, id int64, request DeleteRequest) (Deleted, error) {
	if !request.Confirm {
		return Deleted{}, errors.New("подтвердите удаление трека из папки и библиотеки")
	}
	if !l.maintenance.TryRLock() {
		return Deleted{}, errors.New("библиотека очищается; дождитесь завершения")
	}
	defer l.maintenance.RUnlock()
	track, err := l.Store.Get(id)
	if err != nil {
		return Deleted{}, err
	}
	release, err := l.files.acquire(ctx, track.Path)
	if err != nil {
		return Deleted{}, err
	}
	defer release()
	track, err = l.Store.Get(id)
	if err != nil {
		return Deleted{}, err
	}
	if request.ExpectedPath != track.Path || request.ExpectedModTime != track.ModTime {
		return Deleted{}, errors.New("трек изменился после открытия; откройте его заново перед удалением")
	}
	var staged stagedDeletion
	err = l.Store.Delete(ctx, track, func() error { return staged.prepare(ctx, track) })
	if err != nil {
		if restore := staged.restore(); restore != nil {
			return Deleted{}, fmt.Errorf("удаление не завершено: %v; восстановление файла: %v; файл сохранён в %s", err, restore, staged.path)
		}
		return Deleted{}, err
	}
	// The catalog commit is final: complete removal even after disconnection.
	result := Deleted{AlreadyMissing: staged.missing}
	if err = staged.cleanup(); err != nil {
		result.Warning = "Запись удалена из библиотеки, но очистка файла не завершена: " + err.Error()
	}
	return result, nil
}

type stagedDeletion struct {
	source  string
	dir     string
	path    string
	info    os.FileInfo
	moved   bool
	missing bool
}

func (s *stagedDeletion) prepare(ctx context.Context, track model.Track) error {
	s.source = track.Path
	return retry.Do(ctx, func() error {
		info, err := os.Lstat(track.Path)
		if errors.Is(err, os.ErrNotExist) && s.info == nil {
			s.missing = true
			return nil // An already absent file needs catalog cleanup only.
		}
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return retry.Permanent(errors.New("исходный путь больше не является обычным файлом; удаление отменено"))
		}
		if info.Size() != track.Size || info.ModTime().UnixNano() != track.ModTime || (s.info != nil && !os.SameFile(s.info, info)) {
			return retry.Permanent(errors.New("файл изменён другим приложением; пересканируйте папку перед удалением"))
		}
		s.info = info
		if s.dir == "" {
			s.dir, err = os.MkdirTemp(filepath.Dir(track.Path), ".fftagger-delete-*")
			if err != nil {
				return err
			}
			s.path = filepath.Join(s.dir, filepath.Base(track.Path))
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if err = unchangedFile(track.Path, info); err != nil {
			return err
		}
		moveErr := os.Rename(track.Path, s.path)
		// Check the destination even after an error: a network server may have
		// moved the file before reporting a connection failure.
		staged, statErr := os.Lstat(s.path)
		if moveErr != nil && errors.Is(statErr, os.ErrNotExist) {
			return moveErr
		}
		s.moved = true // On uncertainty preserve this path for recovery.
		if statErr != nil {
			return retry.Permanent(fmt.Errorf("проверка перемещённого файла: %w", statErr))
		}
		if !os.SameFile(info, staged) || staged.Size() != info.Size() || !staged.ModTime().Equal(info.ModTime()) {
			return retry.Permanent(errors.New("файл изменился во время удаления; удаление отменено"))
		}
		return nil
	})
}

func (s *stagedDeletion) restore() error {
	if !s.moved && s.path != "" {
		if _, err := os.Lstat(s.path); err == nil {
			s.moved = true
		} else if !errors.Is(err, os.ErrNotExist) {
			return err // Never clean up a destination with uncertain contents.
		}
	}
	if s.moved {
		info, err := os.Lstat(s.path)
		if err != nil {
			return err
		}
		// Link/copy exclusively so an externally recreated source is never
		// overwritten during rollback. SMB uses the existing copy fallback.
		err = retry.Do(context.Background(), func() error {
			err := backupOriginal(context.Background(), s.path, s.source, info)
			if errors.Is(err, os.ErrExist) {
				return retry.Permanent(err)
			}
			return err
		})
		if err != nil {
			return err
		}
		if err = os.Chtimes(s.source, info.ModTime(), info.ModTime()); err != nil {
			return err
		}
	}
	return s.cleanup()
}

func (s *stagedDeletion) cleanup() error {
	for _, path := range []string{s.path, s.dir} {
		if path == "" {
			continue
		}
		if err := retry.Do(context.Background(), func() error {
			err := os.Remove(path)
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	return nil
}
