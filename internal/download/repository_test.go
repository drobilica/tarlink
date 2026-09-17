package download

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type repositoryRoundTrip func(*http.Request) (*http.Response, error)

func (f repositoryRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type failingBody struct {
	data []byte
	err  error
}

func (b *failingBody) Read(p []byte) (int, error) {
	if len(b.data) == 0 {
		return 0, b.err
	}
	n := copy(p, b.data)
	b.data = b.data[n:]
	return n, nil
}

func (b *failingBody) Close() error { return nil }

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

func TestFetchArtifactFallsBackAfterRepositoryBodyFailures(t *testing.T) {
	data := []byte("repository body fallback")
	digest := fmt.Sprintf("%x", sha256.Sum256(data))
	descriptor := []byte(`{"format":"content-repository","version":1}`)
	for _, test := range []struct {
		name       string
		firstBody  func() io.ReadCloser
		contentLen int64
	}{
		{name: "read error", firstBody: func() io.ReadCloser {
			return &failingBody{data: append([]byte(nil), data...), err: errors.New("connection reset")}
		}, contentLen: -1},
		{name: "truncated", firstBody: func() io.ReadCloser { return io.NopCloser(bytes.NewReader(data[:len(data)-1])) }, contentLen: int64(len(data))},
	} {
		t.Run(test.name, func(t *testing.T) {
			first := "https://first.invalid/repository"
			second := "https://second.invalid/repository"
			client := &Client{
				HTTP: &http.Client{Transport: repositoryRoundTrip(func(r *http.Request) (*http.Response, error) {
					if r.URL.Host == "first.invalid" {
						if r.URL.Path == "/repository/repository.json" {
							return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(descriptor)), ContentLength: int64(len(descriptor)), Request: r}, nil
						}
						return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: test.firstBody(), ContentLength: test.contentLen, Request: r}, nil
					}
					if r.URL.Path == "/repository/repository.json" {
						return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(descriptor)), ContentLength: int64(len(descriptor)), Request: r}, nil
					}
					return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(data)), ContentLength: int64(len(data)), Request: r}, nil
				})},
				Sources: []string{first, second},
			}
			result, err := client.FetchArtifact(context.Background(), ArtifactRequest{URL: "https://upstream.invalid/artifact", Algorithm: "sha256", Digest: digest, Destination: filepath.Join(t.TempDir(), "artifact")})
			if err != nil {
				t.Fatalf("FetchArtifact() error = %v", err)
			}
			if result.Bytes != int64(len(data)) {
				t.Fatalf("result bytes = %d", result.Bytes)
			}
		})
	}
}

func TestFetchArtifactRejectsMalformedSourceConfigurationBeforeUpstream(t *testing.T) {
	requests := 0
	client := &Client{
		HTTP: &http.Client{Transport: repositoryRoundTrip(func(*http.Request) (*http.Response, error) {
			requests++
			return nil, errors.New("unexpected upstream request")
		})},
		SourceConfigError: errors.New("invalid repositories.json"),
	}
	_, err := client.FetchArtifact(context.Background(), ArtifactRequest{
		URL: "https://upstream.invalid/artifact", Algorithm: "sha256", Digest: strings.Repeat("a", 64), Destination: filepath.Join(t.TempDir(), "artifact"),
	})
	if err == nil || !strings.Contains(err.Error(), "repositories.json") {
		t.Fatalf("FetchArtifact() error = %v", err)
	}
	if requests != 0 {
		t.Fatalf("malformed source configuration reached upstream: %d requests", requests)
	}
}

func TestDestinationWriterClassifiesWriteFailures(t *testing.T) {
	want := errors.New("disk full")
	_, err := (destinationWriter{Writer: failingWriter{err: want}}).Write([]byte("bytes"))
	if !errors.Is(err, ErrDestinationWrite) || !errors.Is(err, want) {
		t.Fatalf("destination write error = %v", err)
	}
}

type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }
