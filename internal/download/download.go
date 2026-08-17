// Package download provides bounded HTTPS downloads with transactional files.
package download

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/drobilica/tarlink/internal/filesystem"
)

const (
	DefaultMaxArtifactBytes int64 = 8 << 30
	DefaultMaxRegistryBytes int64 = 64 << 20
	DefaultRedirectLimit          = 5
)

var (
	ErrChecksumMismatch = errors.New("download checksum mismatch")
	ErrTooLarge         = errors.New("download exceeds size limit")
	ErrNetwork          = errors.New("network download failed")
)

type Progress func(downloaded, total int64)

// URLPolicy approves registry URLs and every redirect destination.
type URLPolicy func(*url.URL) bool

type ArtifactRequest struct {
	URL            string
	Algorithm      string
	Digest         string
	Destination    string
	MaxBytes       int64
	ReportProgress Progress
}

type RegistryRequest struct {
	URL            string
	Destination    string
	MaxBytes       int64
	AllowedURL     URLPolicy
	ReportProgress Progress
}

type Result struct {
	Path      string
	Algorithm string
	Digest    string
	Bytes     int64
	Cached    bool
}

type Client struct {
	HTTP          *http.Client
	RedirectLimit int
}

func NewClient() *Client {
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 2 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	}
	return &Client{
		HTTP:          &http.Client{Transport: transport, Timeout: 6 * time.Hour},
		RedirectLimit: DefaultRedirectLimit,
	}
}

func (c *Client) FetchArtifact(ctx context.Context, request ArtifactRequest) (Result, error) {
	if err := validateDigest(request.Algorithm, request.Digest); err != nil {
		return Result{}, err
	}
	_, err := parseHTTPS(request.URL)
	if err != nil {
		return Result{}, err
	}
	if request.MaxBytes <= 0 {
		request.MaxBytes = DefaultMaxArtifactBytes
	}
	if result, ok := validCached(request.Destination, request.Algorithm, request.Digest, request.MaxBytes); ok {
		result.Cached = true
		return result, nil
	}
	return c.fetch(ctx, request.URL, request.Destination, request.MaxBytes, request.Algorithm, request.Digest, nil, request.ReportProgress)
}

// FetchRegistry intentionally has no checksum parameter: the official registry
// endpoint is built into TarLink, and all downloaded registry contents are
// staged and strictly validated before activation.
func (c *Client) FetchRegistry(ctx context.Context, request RegistryRequest) (Result, error) {
	if request.AllowedURL == nil {
		return Result{}, errors.New("registry URL policy is not configured")
	}
	if request.MaxBytes <= 0 {
		request.MaxBytes = DefaultMaxRegistryBytes
	}
	return c.fetch(ctx, request.URL, request.Destination, request.MaxBytes, "", "", request.AllowedURL, request.ReportProgress)
}

func (c *Client) fetch(ctx context.Context, rawURL, destination string, maxBytes int64, algorithm, expected string, allowed URLPolicy, progress Progress) (Result, error) {
	parsed, err := parseHTTPS(rawURL)
	if err != nil {
		return Result{}, err
	}
	if allowed != nil && !allowed(parsed) {
		return Result{}, errors.New("download URL is not the official registry endpoint")
	}
	if (algorithm == "") != (expected == "") {
		return Result{}, errors.New("download verification must specify both algorithm and digest")
	}
	var hasher hash.Hash
	if algorithm != "" {
		hasher, err = newHasher(algorithm, expected)
		if err != nil {
			return Result{}, err
		}
	}
	if destination == "" || !filepath.IsAbs(destination) {
		return Result{}, errors.New("download destination must be an absolute path")
	}
	if maxBytes <= 0 {
		return Result{}, errors.New("download size limit must be positive")
	}
	if c == nil || c.HTTP == nil {
		return Result{}, errors.New("download client is not configured")
	}

	redirects := 0
	limit := c.RedirectLimit
	if limit <= 0 {
		limit = DefaultRedirectLimit
	}
	httpClient := *c.HTTP
	httpClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		redirects++
		if redirects > limit || len(via) > limit {
			return fmt.Errorf("redirect limit of %d exceeded", limit)
		}
		if _, err := parseHTTPS(req.URL.String()); err != nil {
			return fmt.Errorf("invalid HTTPS redirect: %w", err)
		}
		if allowed != nil && !allowed(req.URL) {
			return errors.New("redirect destination is not the official registry endpoint")
		}
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return Result{}, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", "TarLink/0.1")
	// Hash and size checks apply to the exact archived bytes, never an
	// automatically decoded HTTP content encoding.
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := httpClient.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("%w: %v", ErrNetwork, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("%w: HTTP %d", ErrNetwork, resp.StatusCode)
	}
	if resp.ContentLength > maxBytes {
		return Result{}, ErrTooLarge
	}

	if err := filesystem.SecureMkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return Result{}, fmt.Errorf("create download directory: %w", err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".tarlink-download-*")
	if err != nil {
		return Result{}, fmt.Errorf("create temporary download: %w", err)
	}
	temporaryPath := temporary.Name()
	keep := false
	defer func() {
		_ = temporary.Close()
		if !keep {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return Result{}, fmt.Errorf("secure temporary download: %w", err)
	}

	limited := io.LimitReader(resp.Body, maxBytes+1)
	writer := io.Writer(temporary)
	if hasher != nil {
		writer = io.MultiWriter(temporary, hasher)
	}
	written, err := copyWithProgress(writer, limited, resp.ContentLength, progress)
	if err != nil {
		return Result{}, fmt.Errorf("write download: %w", err)
	}
	if written > maxBytes {
		return Result{}, ErrTooLarge
	}
	digest := ""
	if hasher != nil {
		digest = hex.EncodeToString(hasher.Sum(nil))
	}
	if expected != "" && digest != expected {
		return Result{}, fmt.Errorf("%w: expected %s, got %s", ErrChecksumMismatch, expected, digest)
	}
	if err := temporary.Sync(); err != nil {
		return Result{}, fmt.Errorf("flush download: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return Result{}, fmt.Errorf("close download: %w", err)
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return Result{}, fmt.Errorf("publish download: %w", err)
	}
	if err := syncDirectory(filepath.Dir(destination)); err != nil {
		return Result{}, fmt.Errorf("flush download directory: %w", err)
	}
	keep = true
	return Result{Path: destination, Algorithm: algorithm, Digest: digest, Bytes: written}, nil
}

func parseHTTPS(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse download URL: %w", err)
	}
	if parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return nil, errors.New("download URL must use HTTPS and contain no credentials or fragment")
	}
	return parsed, nil
}

func validCached(path, algorithm, expected string, maxBytes int64) (Result, bool) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxBytes {
		return Result{}, false
	}
	file, err := os.Open(path)
	if err != nil {
		return Result{}, false
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openedInfo) || !openedInfo.Mode().IsRegular() || openedInfo.Size() > maxBytes {
		return Result{}, false
	}
	hasher, err := newHasher(algorithm, expected)
	if err != nil {
		return Result{}, false
	}
	written, err := io.Copy(hasher, io.LimitReader(file, maxBytes+1))
	if err != nil || written > maxBytes {
		return Result{}, false
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	if digest != expected {
		_ = file.Close()
		_ = os.Remove(path)
		return Result{}, false
	}
	return Result{Path: path, Algorithm: algorithm, Digest: digest, Bytes: written}, true
}

func validateDigest(algorithm, value string) error {
	_, err := newHasher(algorithm, value)
	return err
}

func newHasher(algorithm, value string) (hash.Hash, error) {
	var hasher hash.Hash
	var size int
	switch algorithm {
	case "sha256":
		hasher = sha256.New()
		size = sha256.Size
	case "sha512":
		hasher = sha512.New()
		size = sha512.Size
	default:
		return nil, fmt.Errorf("unsupported artifact verification algorithm %q", algorithm)
	}
	if len(value) != size*2 || value != strings.ToLower(value) {
		return nil, fmt.Errorf("artifact verification digest must be exactly %d lowercase hexadecimal characters", size*2)
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != size {
		return nil, fmt.Errorf("artifact verification digest must be exactly %d lowercase hexadecimal characters", size*2)
	}
	return hasher, nil
}

func copyWithProgress(destination io.Writer, source io.Reader, total int64, progress Progress) (int64, error) {
	buffer := make([]byte, 128<<10)
	var written int64
	for {
		count, readErr := source.Read(buffer)
		if count > 0 {
			n, writeErr := destination.Write(buffer[:count])
			written += int64(n)
			if progress != nil {
				progress(written, total)
			}
			if writeErr != nil {
				return written, writeErr
			}
			if n != count {
				return written, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return written, nil
			}
			return written, readErr
		}
	}
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
