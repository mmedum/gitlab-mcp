package auth

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ErrLockTimeout means another process held the refresh lock longer
// than the wait allows.
var ErrLockTimeout = errors.New("auth: another gitlab-mcp process is refreshing the sign-in and has not finished")

// lockPoll is how often a waiting process tries the lock again.
const lockPoll = 20 * time.Millisecond

// lockFile takes an exclusive OS lock on path, waiting at most timeout,
// and returns the function that releases it.
//
// The lock is advisory and per open file, so two token sources in one
// process contend exactly as two processes do. The OS drops it when
// the process dies, so a crash mid-refresh leaves nothing to clean up.
func lockFile(ctx context.Context, path string, timeout time.Duration) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("auth: create the lock directory: %w", err)
	}
	// The path is composed by userconfig from a validated profile name.
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600) //nolint:gosec // our own lock file
	if err != nil {
		return nil, fmt.Errorf("auth: open the refresh lock: %w", err)
	}
	deadline := time.Now().Add(timeout)
	for {
		ok, err := tryLock(f)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("auth: take the refresh lock: %w", err)
		}
		if ok {
			return func() {
				_ = unlock(f)
				_ = f.Close()
			}, nil
		}
		if !time.Now().Before(deadline) {
			_ = f.Close()
			return nil, ErrLockTimeout
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}
