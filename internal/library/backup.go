package library

import (
	"context"
	"errors"
	"fftagger/internal/retry"
	"io"
	"os"
	"syscall"
)

func unchangedFile(path string, expected os.FileInfo) error {
	current, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !os.SameFile(expected, current) || current.Size() != expected.Size() || !current.ModTime().Equal(expected.ModTime()) {
		return retry.Permanent(errors.New("файл изменился во время сохранения; оригинал не перезаписан"))
	}
	return nil
}

func backupOriginal(ctx context.Context, source, target string, expected os.FileInfo) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	err := os.Link(source, target)
	if err == nil {
		return nil
	}
	if !errors.Is(err, syscall.ENOTSUP) && !errors.Is(err, syscall.EOPNOTSUPP) && !errors.Is(err, syscall.EXDEV) {
		return err
	}
	return copyBackup(ctx, source, target, expected)
}

// Complete and sync a separate backup before the original is replaced. Create
// exclusively; never overwrite an existing backup, even if names collide.
func copyBackup(ctx context.Context, source, target string, expected os.FileInfo) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = unchangedFile(source, expected); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	opened, err := in.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(expected, opened) {
		return retry.Permanent(errors.New("исходный файл заменён перед созданием резервной копии"))
	}
	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer func() {
		closeErr := out.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			os.Remove(target)
		}
	}()
	// Hide WriterTo/ReaderFrom optimizations to check cancellation between chunks.
	written, err := io.CopyBuffer(struct{ io.Writer }{out}, contextReader{ctx, in}, make([]byte, 1<<20))
	if err != nil {
		return err
	}
	if written != expected.Size() {
		return retry.Permanent(errors.New("размер оригинала изменился при создании резервной копии"))
	}
	if err = unchangedFile(source, expected); err != nil {
		return err
	}
	if err = out.Chmod(expected.Mode().Perm()); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	return ctx.Err()
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
