package registry

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/locking"
)

func registryArchive(t *testing.T, source string) []byte {
	t.Helper()
	var output bytes.Buffer
	gzipWriter := gzip.NewWriter(&output)
	tarWriter := tar.NewWriter(gzipWriter)
	err := filepath.WalkDir(source, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, current)
		if err != nil {
			return err
		}
		name := "tarlink-registry-main"
		if relative != "." {
			name = path.Join(name, filepath.ToSlash(relative))
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = name
		if entry.IsDir() {
			header.Name += "/"
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		file, err := os.Open(current)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tarWriter, file)
		closeErr := file.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func TestSyncIsTransactional(t *testing.T) {
	payload := registryArchive(t, createRegistry(t))
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(payload)
	}))
	defer server.Close()
	root := t.TempDir()
	syncer := &Syncer{
		CacheRoot: filepath.Join(root, "cache", "registry"),
		LocksRoot: filepath.Join(root, "state", "locks"),
		Client:    &download.Client{HTTP: server.Client(), RedirectLimit: 2},
		sourceURL: server.URL,
		allowedURL: func(candidate *url.URL) bool {
			return candidate != nil && candidate.Host == server.Listener.Addr().String()
		},
	}
	if err := syncer.Sync(context.Background()); err != nil {
		t.Fatalf("first Sync() error = %v", err)
	}
	catalog, err := Open(syncer.CacheRoot)
	if err != nil || catalog.Variants["blender"] == nil {
		t.Fatalf("Open() catalog=%#v error=%v", catalog, err)
	}
	before, err := os.Readlink(filepath.Join(syncer.CacheRoot, "current"))
	if err != nil {
		t.Fatal(err)
	}

	payload = []byte("not an archive")
	if err := syncer.Sync(context.Background()); err == nil {
		t.Fatal("invalid registry sync unexpectedly succeeded")
	}
	after, err := os.Readlink(filepath.Join(syncer.CacheRoot, "current"))
	if err != nil || after != before {
		t.Fatalf("current changed after failed sync: before=%q after=%q error=%v", before, after, err)
	}
	entries, err := os.ReadDir(syncer.CacheRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".sync-") {
			t.Fatalf("failed staging directory remains: %s", entry.Name())
		}
	}

	held, err := locking.AcquireRegistry(context.Background(), syncer.LocksRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	syncer.lockTimeout = 20 * time.Millisecond
	if err := syncer.Sync(context.Background()); !errors.Is(err, locking.ErrConflict) {
		t.Fatalf("conflicting registry sync error = %v", err)
	}
}

func TestSyncRetainsOnlyCurrentAndPreviousGenerations(t *testing.T) {
	payload := registryArchive(t, createRegistry(t))
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(payload)
	}))
	defer server.Close()
	root := t.TempDir()
	syncer := &Syncer{
		CacheRoot: filepath.Join(root, "cache", "registry"),
		LocksRoot: filepath.Join(root, "state", "locks"),
		Client:    &download.Client{HTTP: server.Client(), RedirectLimit: 2},
		sourceURL: server.URL,
		allowedURL: func(candidate *url.URL) bool {
			return candidate != nil && candidate.Host == server.Listener.Addr().String()
		},
	}

	targets := make([]string, 0, 3)
	for range 3 {
		if err := syncer.Sync(context.Background()); err != nil {
			t.Fatalf("Sync() error = %v", err)
		}
		target, err := os.Readlink(filepath.Join(syncer.CacheRoot, "current"))
		if err != nil {
			t.Fatal(err)
		}
		targets = append(targets, filepath.Join(syncer.CacheRoot, target))
	}

	if _, err := os.Stat(targets[0]); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old registry generation still exists: %v", err)
	}
	for _, retained := range targets[1:] {
		if info, err := os.Stat(retained); err != nil || !info.IsDir() {
			t.Fatalf("retained registry generation %q info=%v error=%v", retained, info, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(syncer.CacheRoot, "generations"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("generation count = %d, want 2", len(entries))
	}
}

func TestSyncRetainsPreviousGenerationAfterDirectorySyncFailure(t *testing.T) {
	payload := registryArchive(t, createRegistry(t))
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(payload)
	}))
	defer server.Close()
	root := t.TempDir()
	syncer := &Syncer{
		CacheRoot: filepath.Join(root, "cache", "registry"),
		LocksRoot: filepath.Join(root, "state", "locks"),
		Client:    &download.Client{HTTP: server.Client(), RedirectLimit: 2},
		sourceURL: server.URL,
		allowedURL: func(candidate *url.URL) bool {
			return candidate != nil && candidate.Host == server.Listener.Addr().String()
		},
	}
	retained := make([]string, 0, 2)
	for range 2 {
		if err := syncer.Sync(context.Background()); err != nil {
			t.Fatal(err)
		}
		target, err := os.Readlink(filepath.Join(syncer.CacheRoot, "current"))
		if err != nil {
			t.Fatal(err)
		}
		retained = append(retained, target)
	}
	injected := errors.New("injected cache directory sync failure")
	syncer.syncCache = func(string) error { return injected }
	if err := syncer.Sync(context.Background()); !errors.Is(err, injected) {
		t.Fatalf("Sync() error = %v", err)
	}
	after, err := os.Readlink(filepath.Join(syncer.CacheRoot, "current"))
	if err != nil || after != retained[1] {
		t.Fatalf("current before=%q after=%q error=%v", retained[1], after, err)
	}
	if _, err := Open(syncer.CacheRoot); err != nil {
		t.Fatalf("previous registry is unavailable after sync failure: %v", err)
	}
	for _, target := range retained {
		if _, err := os.Stat(filepath.Join(syncer.CacheRoot, target)); err != nil {
			t.Fatalf("retained generation %q was removed: %v", target, err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(syncer.CacheRoot, "generations"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("generation count = %d, want 2 after failed activation", len(entries))
	}
}

func TestSyncKeepsProvisionalGenerationWhenRestoreFails(t *testing.T) {
	payload := registryArchive(t, createRegistry(t))
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(payload)
	}))
	defer server.Close()
	root := t.TempDir()
	syncer := &Syncer{
		CacheRoot: filepath.Join(root, "cache", "registry"),
		LocksRoot: filepath.Join(root, "state", "locks"),
		Client:    &download.Client{HTTP: server.Client(), RedirectLimit: 2},
		sourceURL: server.URL,
		allowedURL: func(candidate *url.URL) bool {
			return candidate != nil && candidate.Host == server.Listener.Addr().String()
		},
	}
	if err := syncer.Sync(context.Background()); err != nil {
		t.Fatal(err)
	}
	previous, err := os.Readlink(filepath.Join(syncer.CacheRoot, "current"))
	if err != nil {
		t.Fatal(err)
	}
	syncFailure := errors.New("injected cache directory sync failure")
	restoreFailure := errors.New("injected pointer restoration failure")
	syncer.syncCache = func(string) error { return syncFailure }
	syncer.restore = func(string, string, string) error { return restoreFailure }
	if err := syncer.Sync(context.Background()); !errors.Is(err, syncFailure) || !errors.Is(err, restoreFailure) {
		t.Fatalf("Sync() error = %v", err)
	}
	current, err := os.Readlink(filepath.Join(syncer.CacheRoot, "current"))
	if err != nil {
		t.Fatal(err)
	}
	if current == previous {
		t.Fatalf("injected pre-rename restoration failure unexpectedly restored %q", previous)
	}
	if _, err := os.Stat(filepath.Join(syncer.CacheRoot, current)); err != nil {
		t.Fatalf("provisional current generation was removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(syncer.CacheRoot, previous)); err != nil {
		t.Fatalf("previous validated generation was removed: %v", err)
	}
	if _, err := Open(syncer.CacheRoot); err != nil {
		t.Fatalf("registry cache has a dangling current pointer: %v", err)
	}
}

func TestSyncRollsBackActivationWhenPruningFails(t *testing.T) {
	payload := registryArchive(t, createRegistry(t))
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write(payload)
	}))
	defer server.Close()
	root := t.TempDir()
	checkedAt := time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	syncer := &Syncer{
		CacheRoot: filepath.Join(root, "cache", "registry"),
		LocksRoot: filepath.Join(root, "state", "locks"),
		Client:    &download.Client{HTTP: server.Client(), RedirectLimit: 2},
		sourceURL: server.URL,
		allowedURL: func(candidate *url.URL) bool {
			return candidate != nil && candidate.Host == server.Listener.Addr().String()
		},
		Now: func() time.Time { return checkedAt },
	}
	if _, err := syncer.SyncWithCheckedAt(context.Background()); err != nil {
		t.Fatal(err)
	}
	previous, err := os.Readlink(filepath.Join(syncer.CacheRoot, "current"))
	if err != nil {
		t.Fatal(err)
	}
	pruneFailure := errors.New("injected generation pruning failure")
	syncer.prune = func(string, string, string) error { return pruneFailure }
	checkedAt = checkedAt.Add(time.Hour)
	if refreshedAt, err := syncer.SyncWithCheckedAt(context.Background()); !errors.Is(err, pruneFailure) || !refreshedAt.IsZero() {
		t.Fatalf("SyncWithCheckedAt() checked-at=%s error=%v", refreshedAt, err)
	}
	current, err := os.Readlink(filepath.Join(syncer.CacheRoot, "current"))
	if err != nil || current != previous {
		t.Fatalf("current before=%q after=%q error=%v", previous, current, err)
	}
	catalog, err := Open(syncer.CacheRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !catalog.FetchedAt.Equal(checkedAt.Add(-time.Hour)) {
		t.Fatalf("failed refresh advanced checked-at to %s", catalog.FetchedAt)
	}
	entries, err := os.ReadDir(filepath.Join(syncer.CacheRoot, "generations"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("generation count = %d, want only the previous generation", len(entries))
	}
}
