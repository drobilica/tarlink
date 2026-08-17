//go:build linux

package locking

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConflictAndIdempotentRelease(t *testing.T) {
	p := t.TempDir() + "/a.lock"
	a, err := Acquire(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := AcquireWithTimeout(ctx, p, 50*time.Millisecond); !errors.Is(err, ErrConflict) {
		t.Fatalf("err=%v", err)
	}
	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
}

func TestRejectsSymlinkLockPath(t *testing.T) {
	d := t.TempDir()
	target := filepath.Join(d, "target")
	if err := os.WriteFile(target, nil, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(d, "lock")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := AcquireWithTimeout(context.Background(), link, 20*time.Millisecond); err == nil {
		t.Fatal("symlink lock accepted")
	}
}
