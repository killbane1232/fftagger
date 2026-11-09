// Package retry retries recoverable I/O while keeping cancellation immediate.
package retry

import (
	"context"
	"errors"
	"time"
)

const Retries = 5
const Delay = time.Second

type permanentError struct{ error }

func (e permanentError) Unwrap() error { return e.error }

// Permanent prevents a retry when repeating the operation cannot safely help.
func Permanent(err error) error { return permanentError{err} }

// Do makes one initial attempt and at most five retries, one second apart.
// Callers must ensure an unsuccessful attempt is safe to repeat.
func Do(ctx context.Context, operation func() error) error {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := operation()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var permanent permanentError
		if errors.As(err, &permanent) || attempt == Retries {
			return err
		}
		timer := time.NewTimer(Delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
