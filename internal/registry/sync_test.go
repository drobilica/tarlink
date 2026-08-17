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
	if err != nil || catalog.Manifests["blender"] == nil {
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
