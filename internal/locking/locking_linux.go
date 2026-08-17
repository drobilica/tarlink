//go:build linux

// Package locking provides bounded advisory locks for TarLink operations.
package locking

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/drobilica/tarlink/internal/filesystem"
)

var ErrConflict = errors.New("lock conflict")

const (
	DefaultTimeout = 2 * time.Second
	pollInterval   = 20 * time.Millisecond
)

// Lock is an acquired advisory lock. Release is safe to call repeatedly.
type Lock struct {
	file *os.File
	once sync.Once
	err  error
}

// Acquire obtains path's exclusive lock, waiting for at most the default
// bounded timeout. Cancellation is checked during polling.
func Acquire(ctx context.Context, path string) (*Lock, error) {
	return AcquireWithTimeout(ctx, path, DefaultTimeout)
}

// AcquireWithTimeout obtains an exclusive lock for at most timeout. A timeout
// is reported as ErrConflict; context cancellation is returned unchanged.
func AcquireWithTimeout(ctx context.Context, path string, timeout time.Duration) (*Lock, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("lock path must be absolute and clean")
	}
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	if err := filesystem.SecureMkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0600)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	if f == nil {
		_ = syscall.Close(fd)
		return nil, errors.New("open lock file")
	}
	var info syscall.Stat_t
	if err := syscall.Fstat(fd, &info); err != nil {
		_ = f.Close()
		return nil, err
	}
	if info.Mode&syscall.S_IFMT != syscall.S_IFREG {
		_ = f.Close()
		return nil, errors.New("lock path is not a regular file")
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	try := func() error { return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) }
	for {
		err = try()
		if err == nil {
			return &Lock{file: f}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
			_ = f.Close()
			return nil, fmt.Errorf("flock: %w", err)
		}
		select {
		case <-ctx.Done():
			_ = f.Close()
			return nil, ctx.Err()
		case <-deadline.C:
			_ = f.Close()
			return nil, ErrConflict
		case <-ticker.C:
		}
	}
}

// Release unlocks and closes the lock. It is idempotent.
func (l *Lock) Release() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		if l.file == nil {
			return
		}
		if err := syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN); err != nil {
			l.err = err
		}
		if err := l.file.Close(); l.err == nil {
			l.err = err
		}
	})
	return l.err
}

// AcquireApp obtains the lock for one safe application ID.
func AcquireApp(ctx context.Context, locksDir, appID string) (*Lock, error) {
	return AcquireAppWithTimeout(ctx, locksDir, appID, DefaultTimeout)
}

func AcquireAppWithTimeout(ctx context.Context, locksDir, appID string, timeout time.Duration) (*Lock, error) {
	if err := filesystem.ValidateID(appID); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(locksDir) || filepath.Clean(locksDir) != locksDir {
		return nil, errors.New("locks directory must be absolute and clean")
	}
	return AcquireWithTimeout(ctx, filepath.Join(locksDir, appID+".lock"), timeout)
}

// AcquireRegistry obtains the lock serializing registry mutations.
func AcquireRegistry(ctx context.Context, locksDir string) (*Lock, error) {
	return AcquireRegistryWithTimeout(ctx, locksDir, DefaultTimeout)
}

func AcquireRegistryWithTimeout(ctx context.Context, locksDir string, timeout time.Duration) (*Lock, error) {
	if !filepath.IsAbs(locksDir) || filepath.Clean(locksDir) != locksDir {
		return nil, errors.New("locks directory must be absolute and clean")
	}
	return AcquireWithTimeout(ctx, filepath.Join(locksDir, "registry.lock"), timeout)
}
