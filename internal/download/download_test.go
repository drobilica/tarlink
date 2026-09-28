package download

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/drobilica/tarlink/internal/version"
)

func TestFetchArtifactVerifiesAndPublishes(t *testing.T) {
	payload := []byte("portable application archive")
	digest := sha256.Sum256(payload)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	destination := filepath.Join(t.TempDir(), "artifact")
	client := &Client{HTTP: server.Client(), RedirectLimit: 2}
	result, err := client.FetchArtifact(context.Background(), ArtifactRequest{
		URL: server.URL, Algorithm: "sha256", Digest: hex.EncodeToString(digest[:]), Destination: destination,
	})
	if err != nil {
		t.Fatalf("FetchArtifact() error = %v", err)
	}
	if result.Bytes != int64(len(payload)) || result.Cached {
		t.Fatalf("unexpected result: %#v", result)
	}
	got, err := os.ReadFile(destination)
	if err != nil || string(got) != string(payload) {
		t.Fatalf("published content = %q, %v", got, err)
	}

	cached, err := client.FetchArtifact(context.Background(), ArtifactRequest{
		URL: server.URL, Algorithm: "sha256", Digest: hex.EncodeToString(digest[:]), Destination: destination,
	})
	if err != nil || !cached.Cached {
		t.Fatalf("cached FetchArtifact() = %#v, %v", cached, err)
	}
}

func TestFetchArtifactChecksumFailureLeavesNoFile(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("wrong"))
	}))
	defer server.Close()
	destination := filepath.Join(t.TempDir(), "artifact")
	client := &Client{HTTP: server.Client()}
	_, err := client.FetchArtifact(context.Background(), ArtifactRequest{
		URL: server.URL, Algorithm: "sha256", Digest: strings.Repeat("0", 64), Destination: destination,
	})
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("FetchArtifact() error = %v", err)
	}
	if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("destination unexpectedly exists: %v", statErr)
	}
}

func TestFetchArtifactSetsVersionedUserAgent(t *testing.T) {
	payload := []byte("versioned user agent")
	digest := sha256.Sum256(payload)
	originalVersion := version.Current
	version.Current = "test-version"
	t.Cleanup(func() { version.Current = originalVersion })
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != "TarLink/test-version" {
			t.Errorf("User-Agent = %q, want %q", got, "TarLink/test-version")
		}
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	_, err := (&Client{HTTP: server.Client()}).FetchArtifact(context.Background(), ArtifactRequest{
		URL: server.URL, Algorithm: "sha256", Digest: hex.EncodeToString(digest[:]), Destination: filepath.Join(t.TempDir(), "artifact"),
	})
	if err != nil {
		t.Fatalf("FetchArtifact() error = %v", err)
	}
}

func TestFetchArtifactUsesDevelopmentUserAgentWhenVersionUnset(t *testing.T) {
	payload := []byte("development user agent")
	digest := sha256.Sum256(payload)
	originalVersion := version.Current
	version.Current = ""
	t.Cleanup(func() { version.Current = originalVersion })
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("User-Agent"); got != "TarLink/development" {
			t.Errorf("User-Agent = %q, want %q", got, "TarLink/development")
		}
		_, _ = w.Write(payload)
	}))
	defer server.Close()

	_, err := (&Client{HTTP: server.Client()}).FetchArtifact(context.Background(), ArtifactRequest{
		URL: server.URL, Algorithm: "sha256", Digest: hex.EncodeToString(digest[:]), Destination: filepath.Join(t.TempDir(), "artifact"),
	})
	if err != nil {
		t.Fatalf("FetchArtifact() error = %v", err)
	}
}

func TestFetchArtifactAllowsHTTPSRedirectsBeforeDigestCheck(t *testing.T) {
	payload := []byte("redirected archive")
	digest := sha256.Sum256(payload)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/redirect" {
			http.Redirect(writer, request, "/archive", http.StatusFound)
			return
		}
		_, _ = writer.Write(payload)
	}))
	defer server.Close()
	result, err := (&Client{HTTP: server.Client()}).FetchArtifact(context.Background(), ArtifactRequest{
		URL: server.URL + "/redirect", Algorithm: "sha256", Digest: hex.EncodeToString(digest[:]), Destination: filepath.Join(t.TempDir(), "artifact"),
	})
	if err != nil {
		t.Fatalf("FetchArtifact() redirect error = %v", err)
	}
	if result.Bytes != int64(len(payload)) {
		t.Fatalf("redirected result bytes = %d", result.Bytes)
	}
}

func TestFetchArtifactRejectsHTTPSDowngrade(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/redirect" {
			http.Redirect(writer, request, "http://example.com/archive", http.StatusFound)
			return
		}
		_, _ = writer.Write([]byte("unexpected"))
	}))
	defer server.Close()
	_, err := (&Client{HTTP: server.Client()}).FetchArtifact(context.Background(), ArtifactRequest{
		URL: server.URL + "/redirect", Algorithm: "sha256", Digest: strings.Repeat("0", 64), Destination: filepath.Join(t.TempDir(), "artifact"),
	})
	if err == nil {
		t.Fatal("HTTPS downgrade unexpectedly accepted")
	}
}

func TestFetchArtifactRejectsUnsupportedVerification(t *testing.T) {
	client := NewClient()
	base := ArtifactRequest{URL: "https://example.com/archive.zip", Destination: filepath.Join(t.TempDir(), "artifact")}
	for _, test := range []struct {
		algorithm string
		digest    string
	}{
		{algorithm: "md5", digest: strings.Repeat("0", 32)},
		{algorithm: "sha1", digest: strings.Repeat("0", 40)},
		{algorithm: "sha256", digest: strings.Repeat("A", 64)},
	} {
		base.Algorithm, base.Digest = test.algorithm, test.digest
		if _, err := client.FetchArtifact(context.Background(), base); err == nil {
			t.Fatalf("algorithm %q unexpectedly accepted", test.algorithm)
		}
	}
}

func TestFetchArtifactVerifiesAndCachesSHA512(t *testing.T) {
	payload := []byte("sha512 application archive")
	digest := sha512.Sum512(payload)
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	destination := filepath.Join(t.TempDir(), "artifact")
	client := &Client{HTTP: server.Client()}
	request := ArtifactRequest{URL: server.URL, Algorithm: "sha512", Digest: hex.EncodeToString(digest[:]), Destination: destination}
	result, err := client.FetchArtifact(context.Background(), request)
	if err != nil || result.Cached || result.Digest != request.Digest {
		t.Fatalf("downloaded SHA-512 result = %#v, error = %v", result, err)
	}
	cached, err := client.FetchArtifact(context.Background(), request)
	if err != nil || !cached.Cached || requests != 1 {
		t.Fatalf("cached SHA-512 result = %#v, error = %v, requests = %d", cached, err, requests)
	}
}

func TestFetchArtifactRejectsIncorrectSHA512(t *testing.T) {
	payload := []byte("sha512 application archive")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	_, err := (&Client{HTTP: server.Client()}).FetchArtifact(context.Background(), ArtifactRequest{
		URL: server.URL, Algorithm: "sha512", Digest: strings.Repeat("0", 128),
		Destination: filepath.Join(t.TempDir(), "artifact"),
	})
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("FetchArtifact() error = %v, want checksum mismatch", err)
	}
}

func TestFetchArtifactRejectsHTTP(t *testing.T) {
	client := NewClient()
	destination := filepath.Join(t.TempDir(), "artifact")
	base := ArtifactRequest{Algorithm: "sha256", Digest: strings.Repeat("0", 64), Destination: destination}
	base.URL = "http://example.com/archive.zip"
	if _, err := client.FetchArtifact(context.Background(), base); err == nil {
		t.Fatal("HTTP URL unexpectedly accepted")
	}
}

func TestAcquireVerifiedPublishesCanonicalCacheAndReuses(t *testing.T) {
	payload := []byte("canonical verified blob")
	digest := sha256.Sum256(payload)
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	cache := filepath.Join(t.TempDir(), "artifacts")
	client := &Client{HTTP: server.Client(), RedirectLimit: 2, ArtifactCache: cache}
	request := VerifiedRequest{URL: server.URL, Algorithm: "sha256", Digest: hex.EncodeToString(digest[:])}

	first, err := client.AcquireVerified(context.Background(), request)
	if err != nil {
		t.Fatalf("AcquireVerified() error = %v", err)
	}
	wantPath := filepath.Join(cache, "v1", "sha256", hex.EncodeToString(digest[:]))
	if first.Cached || first.Path != wantPath || first.Bytes != int64(len(payload)) {
		t.Fatalf("first verified = %#v, want path %s", first, wantPath)
	}
	content, err := io.ReadAll(first.File)
	if err != nil || !bytes.Equal(content, payload) {
		t.Fatalf("verified content = %q, %v", content, err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := client.AcquireVerified(context.Background(), request)
	if err != nil || !second.Cached {
		t.Fatalf("cached AcquireVerified() = %#v, %v", second, err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("artifact requests = %d, want 1 (canonical cache reuse)", requests)
	}
}

func TestAcquireVerifiedRequiresAbsoluteCacheRoot(t *testing.T) {
	base := VerifiedRequest{URL: "https://example.com/archive", Algorithm: "sha256", Digest: strings.Repeat("0", 64)}
	if _, err := (&Client{HTTP: NewClient().HTTP}).AcquireVerified(context.Background(), base); err == nil {
		t.Fatal("missing cache root unexpectedly accepted")
	}
	if _, err := (&Client{HTTP: NewClient().HTTP, ArtifactCache: "relative/artifacts"}).AcquireVerified(context.Background(), base); err == nil {
		t.Fatal("relative cache root unexpectedly accepted")
	}
}

func TestAcquireVerifiedChecksumFailureLeavesNoCacheObject(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("wrong bytes"))
	}))
	defer server.Close()
	cache := filepath.Join(t.TempDir(), "artifacts")
	client := &Client{HTTP: server.Client(), ArtifactCache: cache}
	digest := strings.Repeat("0", 64)
	_, err := client.AcquireVerified(context.Background(), VerifiedRequest{URL: server.URL, Algorithm: "sha256", Digest: digest})
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("AcquireVerified() error = %v, want checksum mismatch", err)
	}
	if _, statErr := os.Stat(filepath.Join(cache, "v1", "sha256", digest)); !os.IsNotExist(statErr) {
		t.Fatalf("cache object unexpectedly exists: %v", statErr)
	}
}

func TestAcquireVerifiedRejectsSymlinkedCacheObject(t *testing.T) {
	payload := []byte("hostile cache object")
	digest := sha256.Sum256(payload)
	other := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(other, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(t.TempDir(), "artifacts")
	directory := filepath.Join(cache, "v1", "sha256")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, filepath.Join(directory, hex.EncodeToString(digest[:]))); err != nil {
		t.Fatal(err)
	}
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	client := &Client{HTTP: server.Client(), ArtifactCache: cache}
	_, err := client.AcquireVerified(context.Background(), VerifiedRequest{URL: server.URL, Algorithm: "sha256", Digest: hex.EncodeToString(digest[:])})
	if !errors.Is(err, ErrUnsafeCache) {
		t.Fatalf("symlinked cache object error = %v, want unsafe cache", err)
	}
	if requests != 0 {
		t.Fatalf("artifact requests = %d, want 0 (unsafe leaf must not be repaired)", requests)
	}
	got, readErr := os.ReadFile(other)
	if readErr != nil || !bytes.Equal(got, payload) {
		t.Fatalf("symlink target changed: %q, %v", got, readErr)
	}
}

func TestAcquireVerifiedRejectsHardlinkAndDirectoryObjects(t *testing.T) {
	payload := []byte("hostile object")
	digest := sha256.Sum256(payload)
	cache := filepath.Join(t.TempDir(), "artifacts")
	objectDir := filepath.Join(cache, "v1", "sha256")
	if err := os.MkdirAll(objectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	digestText := hex.EncodeToString(digest[:])
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(outside, filepath.Join(objectDir, digestText)); err != nil {
		t.Fatal(err)
	}
	client := &Client{HTTP: NewClient().HTTP, ArtifactCache: cache}
	request := VerifiedRequest{URL: "https://example.com/archive", Algorithm: "sha256", Digest: digestText}
	if _, err := client.AcquireVerified(context.Background(), request); !errors.Is(err, ErrUnsafeCache) {
		t.Fatalf("hardlinked object error = %v, want unsafe cache", err)
	}
	if err := os.Remove(filepath.Join(objectDir, digestText)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(objectDir, digestText), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := client.AcquireVerified(context.Background(), request); !errors.Is(err, ErrUnsafeCache) {
		t.Fatalf("directory object error = %v, want unsafe cache", err)
	}
	if err := os.Remove(filepath.Join(objectDir, digestText)); err != nil {
		t.Fatal(err)
	}
	outsideDir := filepath.Join(t.TempDir(), "outside-dir")
	if err := os.Mkdir(outsideDir, 0o700); err != nil {
		t.Fatal(err)
	}
	parentCache := filepath.Join(t.TempDir(), "parent-cache")
	if err := os.Mkdir(parentCache, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsideDir, filepath.Join(parentCache, "v1")); err != nil {
		t.Fatal(err)
	}
	client.ArtifactCache = parentCache
	if _, err := client.AcquireVerified(context.Background(), request); !errors.Is(err, ErrDestinationWrite) {
		t.Fatalf("symlinked cache parent error = %v, want destination write", err)
	}
}

func TestAcquireVerifiedRejectsSpecialObject(t *testing.T) {
	cache := filepath.Join(t.TempDir(), "artifacts")
	objectDir := filepath.Join(cache, "v1", "sha256")
	if err := os.MkdirAll(objectDir, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("0", 64)
	object := filepath.Join(objectDir, digest)
	if err := syscall.Mkfifo(object, 0o600); err != nil {
		t.Skipf("named pipes unavailable: %v", err)
	}
	client := &Client{HTTP: NewClient().HTTP, ArtifactCache: cache}
	if _, err := client.AcquireVerified(context.Background(), VerifiedRequest{URL: "https://example.com/archive", Algorithm: "sha256", Digest: digest}); !errors.Is(err, ErrUnsafeCache) {
		t.Fatalf("special cache object error = %v, want unsafe cache", err)
	}
}

func TestAcquireVerifiedStricterLimitDoesNotEvictValidObject(t *testing.T) {
	payload := []byte("bounded canonical object")
	digest := sha256.Sum256(payload)
	requests := 0
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	cache := filepath.Join(t.TempDir(), "artifacts")
	client := &Client{HTTP: server.Client(), ArtifactCache: cache}
	request := VerifiedRequest{URL: server.URL, Algorithm: "sha256", Digest: hex.EncodeToString(digest[:])}
	verified, err := client.AcquireVerified(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	_ = verified.Close()
	if _, err := client.AcquireVerified(context.Background(), VerifiedRequest{
		URL: server.URL, Algorithm: request.Algorithm, Digest: request.Digest, MaxBytes: int64(len(payload) - 1),
	}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("strict warm-cache acquisition error = %v, want size error", err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want one bounded retry after the warm-cache miss", requests)
	}
	if got, err := os.ReadFile(filepath.Join(cache, "v1", "sha256", request.Digest)); err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("valid cache object changed: %q, %v", got, err)
	}
}

func TestAcquireVerifiedCancellationLeavesNoCacheObject(t *testing.T) {
	payload := bytes.Repeat([]byte("cancelled"), 1024)
	digest := sha256.Sum256(payload)
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	cache := filepath.Join(t.TempDir(), "artifacts")
	client := &Client{HTTP: server.Client(), ArtifactCache: cache}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		_, err := client.AcquireVerified(ctx, VerifiedRequest{URL: server.URL, Algorithm: "sha256", Digest: hex.EncodeToString(digest[:])})
		errCh <- err
	}()
	<-started
	cancel()
	close(release)
	if err := <-errCh; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled acquisition error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(cache, "v1", "sha256", hex.EncodeToString(digest[:]))); !os.IsNotExist(err) {
		t.Fatalf("cancelled acquisition published cache object: %v", err)
	}
}

func TestFetchRegistryEnforcesLimit(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("too large"))
	}))
	defer server.Close()
	client := &Client{HTTP: server.Client()}
	_, err := client.FetchRegistry(context.Background(), RegistryRequest{
		URL: server.URL, Destination: filepath.Join(t.TempDir(), "registry"), MaxBytes: 3,
		AllowedURL: func(*url.URL) bool { return true },
	})
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("FetchRegistry() error = %v", err)
	}
}
