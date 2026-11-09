package library

import (
	"context"
	"fftagger/internal/syncutil"
	"sync"
)

type fileLock struct {
	mutex syncutil.Mutex
	users int
}
type fileLocks struct {
	mu      sync.Mutex
	entries map[string]*fileLock
}

// Only operations on the same path exclude each other. Entries are removed
// when no longer used, so a large library does not accumulate lock objects.
func (f *fileLocks) acquire(ctx context.Context, path string) (func(), error) {
	f.mu.Lock()
	if f.entries == nil {
		f.entries = make(map[string]*fileLock)
	}
	entry := f.entries[path]
	if entry == nil {
		entry = &fileLock{}
		f.entries[path] = entry
	}
	entry.users++
	f.mu.Unlock()
	drop := func() {
		f.mu.Lock()
		entry.users--
		if entry.users == 0 {
			delete(f.entries, path)
		}
		f.mu.Unlock()
	}
	if err := entry.mutex.LockContext(ctx); err != nil {
		drop()
		return nil, err
	}
	return func() { entry.mutex.Unlock(); drop() }, nil
}
