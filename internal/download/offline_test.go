package download

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func offlineTestRepository(t *testing.T, root, digest string, body []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "v1", "sha256"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "repository.json"), []byte(`{"format":"content-repository","version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if body != nil {
		if err := os.WriteFile(filepath.Join(root, "v1", "sha256", digest), body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestOfflineSkipsHTTPSAndRepairsCacheFromVerifiedLocalSource(t *testing.T) {
	body := []byte("approved local artifact")
	digest := fmt.Sprintf("%x", sha256.Sum256(body))
	for _, kind := range []string{"cache", "local", "corrupt-cache", "corrupt-first-source"} {
		t.Run(kind, func(t *testing.T) {
			cache := filepath.Join(t.TempDir(), "artifacts")
			first, second := t.TempDir(), t.TempDir()
			offlineTestRepository(t, first, digest, []byte("bad source"))
			offlineTestRepository(t, second, digest, body)
			calls := 0
			client := &Client{Offline: true, ArtifactCache: cache, HTTP: &http.Client{Transport: repositoryRoundTrip(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("unexpected HTTP") })}, Sources: []string{"https://mirror.invalid/content", second}}
			if kind == "cache" || kind == "corrupt-cache" {
				path, _ := artifactCachePath(cache, "sha256", digest)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				content := body
				if kind == "corrupt-cache" {
					content = []byte("corrupt cache")
				}
				if err := os.WriteFile(path, content, 0o600); err != nil {
					t.Fatal(err)
				}
				if kind == "cache" {
					client.Sources = []string{"https://mirror.invalid/content"}
				}
			}
			if kind == "corrupt-first-source" {
				client.Sources = []string{"https://mirror.invalid/content", first, second}
			}
			verified, err := client.AcquireVerified(context.Background(), VerifiedRequest{URL: "https://upstream.invalid/artifact", Algorithm: "sha256", Digest: digest})
			if err != nil {
				t.Fatal(err)
			}
			defer verified.Close()
			got, err := io.ReadAll(verified.File)
			if err != nil || string(got) != string(body) || calls != 0 {
				t.Fatalf("bytes=%q err=%v HTTP=%d", got, err, calls)
			}
		})
	}
}

func TestOfflineRejectsEveryNetworkEntryPointWithoutDestinationWrites(t *testing.T) {
	ctx := context.Background()
	calls := 0
	client := &Client{Offline: true, ArtifactCache: filepath.Join(t.TempDir(), "cache"), HTTP: &http.Client{Transport: repositoryRoundTrip(func(*http.Request) (*http.Response, error) { calls++; return nil, errors.New("unexpected HTTP") })}, Sources: []string{"https://mirror.invalid/content"}}
	path := filepath.Join(t.TempDir(), "result")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte("expected")))
	operations := []func() error{
		func() error {
			_, err := client.FetchFile(ctx, FileRequest{URL: "https://upstream.invalid/file", Destination: path})
			return err
		},
		func() error {
			_, err := client.FetchRegistry(ctx, RegistryRequest{URL: "https://upstream.invalid/registry", Destination: path, AllowedURL: func(*url.URL) bool { return true }})
			return err
		},
		func() error {
			_, err := client.FetchArtifact(ctx, ArtifactRequest{URL: "https://upstream.invalid/artifact", Algorithm: "sha256", Digest: digest, Destination: path})
			return err
		},
		func() error {
			_, err := client.AcquireVerified(ctx, VerifiedRequest{URL: "https://upstream.invalid/artifact", Algorithm: "sha256", Digest: digest})
			return err
		},
	}
	for i, operation := range operations {
		if err := operation(); !errors.Is(err, ErrOffline) {
			t.Fatalf("operation %d: %v", i, err)
		}
		body, err := os.ReadFile(path)
		if err != nil || string(body) != "original" {
			t.Fatalf("destination changed: %q %v", body, err)
		}
	}
	if calls != 0 {
		t.Fatalf("offline issued %d HTTP requests", calls)
	}
}

func TestOfflineUnsafeCacheCancellationAndSourceConfigurationFailClosed(t *testing.T) {
	body := []byte("local artifact")
	digest := fmt.Sprintf("%x", sha256.Sum256(body))
	for _, kind := range []string{"symlink", "directory", "hardlink", "cancelled", "config", "size"} {
		t.Run(kind, func(t *testing.T) {
			cache := filepath.Join(t.TempDir(), "cache")
			source := t.TempDir()
			offlineTestRepository(t, source, digest, body)
			client := &Client{Offline: true, ArtifactCache: cache, Sources: []string{source}}
			path, _ := artifactCachePath(cache, "sha256", digest)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(t.TempDir(), "user-file")
			if err := os.WriteFile(outside, body, 0o600); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			request := VerifiedRequest{URL: "https://upstream.invalid/artifact", Algorithm: "sha256", Digest: digest}
			switch kind {
			case "symlink":
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
			case "hardlink":
				if err := os.Link(outside, path); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "config":
				client.SourceConfigError = errors.New("malformed repositories.json")
			case "size":
				request.MaxBytes = 1
			}
			if artifact, err := client.AcquireVerified(ctx, request); err == nil {
				artifact.Close()
				t.Fatal("unsafe/invalid acquisition accepted")
			}
			content, err := os.ReadFile(outside)
			if err != nil || string(content) != string(body) {
				t.Fatalf("unrelated file changed: %q %v", content, err)
			}
		})
	}
}

func TestOfflineSHA512LocalArtifact(t *testing.T) {
	body := []byte("SHA-512 approved artifact")
	digest := fmt.Sprintf("%x", sha512.Sum512(body))
	root := t.TempDir()
	offlineTestRepository(t, root, fmt.Sprintf("%x", sha256.Sum256(nil)), nil)
	if err := os.MkdirAll(filepath.Join(root, "v1", "sha512"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "v1", "sha512", digest), body, 0o600); err != nil {
		t.Fatal(err)
	}
	client := &Client{Offline: true, ArtifactCache: filepath.Join(t.TempDir(), "cache"), Sources: []string{root}}
	artifact, err := client.AcquireVerified(context.Background(), VerifiedRequest{URL: "https://upstream.invalid/archive", Algorithm: "sha512", Digest: digest})
	if err != nil {
		t.Fatal(err)
	}
	defer artifact.Close()
	content, err := io.ReadAll(artifact.File)
	if err != nil || string(content) != string(body) {
		t.Fatalf("SHA-512 local artifact=%q %v", content, err)
	}
}
