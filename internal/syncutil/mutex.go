// Package syncutil provides the cancellable locks used by file operations.
package syncutil

import (
	"context"
	"sync"
)

// Mutex has a usable zero value, like sync.Mutex, but a waiting import can
// abandon the lock immediately when the user stops scanning.
type Mutex struct {
	once sync.Once
	gate chan struct{}
}

func (m *Mutex) LockContext(ctx context.Context) error {
	m.once.Do(func() { m.gate = make(chan struct{}, 1) })
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case m.gate <- struct{}{}:
		if err := ctx.Err(); err != nil {
			m.Unlock()
			return err
		}
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (m *Mutex) Lock()   { _ = m.LockContext(context.Background()) }
func (m *Mutex) Unlock() { <-m.gate }
