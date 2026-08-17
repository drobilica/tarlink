package download

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
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

func TestFetchArtifactSupportsSHA512(t *testing.T) {
	payload := []byte("portable application archive")
	digest := sha512.Sum512(payload)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	result, err := (&Client{HTTP: server.Client()}).FetchArtifact(context.Background(), ArtifactRequest{
		URL: server.URL, Algorithm: "sha512", Digest: hex.EncodeToString(digest[:]), Destination: filepath.Join(t.TempDir(), "artifact"),
	})
	if err != nil {
		t.Fatalf("FetchArtifact() SHA-512 error = %v", err)
	}
	if result.Algorithm != "sha512" || result.Digest != hex.EncodeToString(digest[:]) {
		t.Fatalf("unexpected SHA-512 result: %#v", result)
	}
}

func TestFetchArtifactRejectsSHA512Mismatch(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("wrong SHA-512 bytes"))
	}))
	defer server.Close()
	destination := filepath.Join(t.TempDir(), "artifact")
	_, err := (&Client{HTTP: server.Client()}).FetchArtifact(context.Background(), ArtifactRequest{
		URL: server.URL, Algorithm: "sha512", Digest: strings.Repeat("0", 128), Destination: destination,
	})
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("FetchArtifact() error = %v", err)
	}
	if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("destination unexpectedly exists: %v", statErr)
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
		{algorithm: "sha512", digest: strings.Repeat("0", 64)},
		{algorithm: "sha256", digest: strings.Repeat("A", 64)},
	} {
		base.Algorithm, base.Digest = test.algorithm, test.digest
		if _, err := client.FetchArtifact(context.Background(), base); err == nil {
			t.Fatalf("algorithm %q unexpectedly accepted", test.algorithm)
		}
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
