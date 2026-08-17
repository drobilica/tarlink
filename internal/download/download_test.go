package download

import (
	"context"
	"crypto/sha256"
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
		URL: server.URL, SHA256: hex.EncodeToString(digest[:]), Destination: destination,
		AllowedURL: func(candidate *url.URL) bool { return candidate.Host == server.Listener.Addr().String() },
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
		URL: server.URL, SHA256: hex.EncodeToString(digest[:]), Destination: destination,
		AllowedURL: func(*url.URL) bool { return true },
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
		URL: server.URL, SHA256: strings.Repeat("0", 64), Destination: destination,
		AllowedURL: func(*url.URL) bool { return true },
	})
	if !errors.Is(err, ErrChecksumMismatch) {
		t.Fatalf("FetchArtifact() error = %v", err)
	}
	if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("destination unexpectedly exists: %v", statErr)
	}
}

func TestFetchArtifactRejectsHTTPAndUnapprovedURL(t *testing.T) {
	client := NewClient()
	destination := filepath.Join(t.TempDir(), "artifact")
	base := ArtifactRequest{SHA256: strings.Repeat("0", 64), Destination: destination, AllowedURL: func(*url.URL) bool { return true }}
	base.URL = "http://example.com/archive.zip"
	if _, err := client.FetchArtifact(context.Background(), base); err == nil {
		t.Fatal("HTTP URL unexpectedly accepted")
	}
	base.URL = "https://example.com/archive.zip"
	base.AllowedURL = func(*url.URL) bool { return false }
	if _, err := client.FetchArtifact(context.Background(), base); err == nil {
		t.Fatal("unapproved URL unexpectedly accepted")
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
