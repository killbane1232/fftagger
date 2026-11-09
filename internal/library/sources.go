package library

import (
	"errors"
	"fftagger/internal/store"
	"os"
	"path/filepath"
	"strings"
)

func resolveFolder(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("выберите папку")
	}
	root, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("нужно выбрать папку")
	}
	return root, nil
}
func (l *Library) AddSource(root string) (store.Source, error) {
	root, err := resolveFolder(root)
	if err != nil {
		return store.Source{}, err
	}
	return l.Store.RememberSource(root, false)
}
func (l *Library) RemoveSource(id int64) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	source, err := l.Store.Source(id)
	if err != nil {
		return err
	}
	if l.progress.Running && l.progress.Root == source.Path {
		return errors.New("сначала остановите сканирование этой папки")
	}
	return l.Store.RemoveSource(id)
}
