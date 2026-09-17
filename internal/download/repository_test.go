package download

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type repositoryRoundTrip func(*http.Request) (*http.Response, error)

func (f repositoryRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestFetchArtifactUsesOrderedRepositoriesAndSkipsCorruptBytes(t *testing.T) {
	data := []byte("repository artifact bytes")
	digest := sha256.Sum256(data)
	name := fmt.Sprintf("%x", digest)
	first := t.TempDir()
	second := t.TempDir()
	for _, root := range []string{first, second} {
		if err := os.MkdirAll(filepath.Join(root, "v1", "sha256"), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "repository.json"), []byte(`{"format":"content-repository","version":1}`), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(first, "v1", "sha256", name), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(second, "v1", "sha256", name), data, 0600); err != nil {
		t.Fatal(err)
	}
	upstreamRequests := 0
	diagnostics := []string{}
	client := &Client{
		HTTP: &http.Client{Transport: repositoryRoundTrip(func(r *http.Request) (*http.Response, error) {
			upstreamRequests++
			return nil, errors.New("upstream unavailable")
		})},
		Sources:          []string{first, second},
		SourceDiagnostic: func(value string) { diagnostics = append(diagnostics, value) },
	}
	destination := filepath.Join(t.TempDir(), "artifact.tar.gz")
	result, err := client.FetchArtifact(context.Background(), ArtifactRequest{
		URL: "https://upstream.invalid/artifact.tar.gz", Algorithm: "sha256", Digest: name, Destination: destination,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Bytes != int64(len(data)) || upstreamRequests != 0 {
		t.Fatalf("result=%#v upstream requests=%d", result, upstreamRequests)
	}
	if got, err := os.ReadFile(destination); err != nil || string(got) != string(data) {
		t.Fatalf("destination=%q err=%v", got, err)
	}
	if len(diagnostics) == 0 || !strings.Contains(diagnostics[0], first) {
		t.Fatalf("diagnostics=%v", diagnostics)
	}
}

func TestFetchArtifactLeavesCorruptDestinationWhenAllSourcesFail(t *testing.T) {
	digest := strings.Repeat("a", 64)
	destination := filepath.Join(t.TempDir(), "artifact")
	if err := os.WriteFile(destination, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	client := &Client{
		HTTP: &http.Client{Transport: repositoryRoundTrip(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("offline")
		})},
		Sources: []string{filepath.Join(t.TempDir(), "missing")},
	}
	_, err := client.FetchArtifact(context.Background(), ArtifactRequest{
		URL: "https://upstream.invalid/artifact", Algorithm: "sha256", Digest: digest, Destination: destination,
	})
	if err == nil {
		t.Fatal("failed acquisition unexpectedly succeeded")
	}
	got, readErr := os.ReadFile(destination)
	if readErr != nil || string(got) != "corrupt" {
		t.Fatalf("corrupt destination changed: %q err=%v", got, readErr)
	}
}

func TestFetchArtifactDoesNotAcceptInvalidRepositoryDescriptor(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "v1", "sha256"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "repository.json"), []byte(`{"format":"other","version":1}`), 0600); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("b", 64)
	client := &Client{
		HTTP: &http.Client{Transport: repositoryRoundTrip(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("upstream unavailable")
		})},
		Sources: []string{root},
	}
	_, err := client.FetchArtifact(context.Background(), ArtifactRequest{URL: "https://upstream.invalid/artifact", Algorithm: "sha256", Digest: digest, Destination: filepath.Join(t.TempDir(), "artifact")})
	if err == nil || !strings.Contains(err.Error(), "unsupported repository descriptor") {
		t.Fatalf("error=%v", err)
	}
}
