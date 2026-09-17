// Package download provides bounded HTTPS downloads with transactional files.
package download

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
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
	"syscall"
	"time"

	"github.com/drobilica/tarlink/internal/checksum"
	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/version"
)

const (
	DefaultMaxArtifactBytes      int64 = 8 << 30
	DefaultMaxRegistryBytes      int64 = 64 << 20
	maxRepositoryDescriptorBytes int64 = 4 << 10
	DefaultRedirectLimit               = 5
	DefaultMaxArtifactSources          = 16
)

var (
	ErrChecksumMismatch = errors.New("download checksum mismatch")
	ErrTooLarge         = errors.New("download exceeds size limit")
	ErrNetwork          = errors.New("network download failed")
	ErrDestinationWrite = errors.New("download destination write failed")
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

// FileRequest downloads an HTTPS file without a digest. Callers must apply
// their own verification to the returned file before using its contents.
type FileRequest struct {
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
	// Sources are ordered static content repositories. Invalid sources and
	// ordinary acquisition failures are recorded and skipped.
	Sources          []string
	SourceDiagnostic func(string)
	// SourceConfigError prevents acquisition from silently ignoring a malformed
	// optional repositories.json while allowing local-only commands to start.
	SourceConfigError error
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
	if c != nil && c.SourceConfigError != nil {
		return Result{}, fmt.Errorf("load repository sources: %w", c.SourceConfigError)
	}
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
	var failures []string
	for index, source := range c.Sources {
		if index >= DefaultMaxArtifactSources {
			message := fmt.Sprintf("configured repository source limit of %d reached", DefaultMaxArtifactSources)
			if c.SourceDiagnostic != nil {
				c.SourceDiagnostic(message)
			}
			failures = append(failures, message)
			break
		}
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		if err := c.validateSource(ctx, source, request.Destination); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return Result{}, err
			}
			if errors.Is(err, ErrDestinationWrite) {
				return Result{}, err
			}
			failures = append(failures, source+": "+err.Error())
			if c.SourceDiagnostic != nil {
				c.SourceDiagnostic(failures[len(failures)-1])
			}
			continue
		}
		candidate, err := sourceObject(source, request.Algorithm, request.Digest)
		if err != nil {
			failures = append(failures, source+": "+err.Error())
			if c.SourceDiagnostic != nil {
				c.SourceDiagnostic(failures[len(failures)-1])
			}
			continue
		}
		var result Result
		if filepath.IsAbs(source) {
			result.Bytes, err = copyLocalArtifact(ctx, source, request.Destination, request.MaxBytes, request.Algorithm, request.Digest)
			result.Path, result.Algorithm, result.Digest = request.Destination, request.Algorithm, request.Digest
		} else if strings.HasPrefix(candidate, "https://") {
			result, err = c.fetch(ctx, candidate, request.Destination, request.MaxBytes, request.Algorithm, request.Digest, nil, request.ReportProgress)
		}
		if err == nil {
			return result, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Result{}, err
		}
		if errors.Is(err, ErrDestinationWrite) {
			return Result{}, err
		}
		failures = append(failures, source+": "+err.Error())
		if c.SourceDiagnostic != nil {
			c.SourceDiagnostic(failures[len(failures)-1])
		}
	}
	result, err := c.fetch(ctx, request.URL, request.Destination, request.MaxBytes, request.Algorithm, request.Digest, nil, request.ReportProgress)
	if err != nil && len(failures) > 0 {
		return Result{}, fmt.Errorf("artifact acquisition failed; attempted sources: %s; upstream: %w", strings.Join(failures, "; "), err)
	}
	return result, err
}

func sourceObject(raw, algorithm, digest string) (string, error) {
	if filepath.IsAbs(raw) {
		return filepath.Join(raw, "v1", algorithm, digest), nil
	}
	u, err := parseHTTPS(raw)
	if err != nil || u.RawQuery != "" {
		return "", errors.New("source must be an absolute filesystem path or HTTPS URL without a query")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/v1/" + algorithm + "/" + digest
	return u.String(), nil
}

type repositoryDescriptor struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
}

func (c *Client) validateSource(ctx context.Context, source, destination string) error {
	if filepath.IsAbs(source) {
		return validateRepositoryRoot(source)
	}
	parsed, err := parseHTTPS(source)
	if err != nil || parsed.RawQuery != "" {
		return errors.New("source must be an absolute filesystem path or HTTPS URL without a query")
	}
	if destination == "" || !filepath.IsAbs(destination) {
		return errors.New("download destination must be absolute")
	}
	if err := filesystem.SecureMkdirAll(filepath.Dir(destination), 0700); err != nil {
		return fmt.Errorf("%w: prepare descriptor staging: %v", ErrDestinationWrite, err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".tarlink-repository-descriptor-*")
	if err != nil {
		return fmt.Errorf("%w: create descriptor staging: %v", ErrDestinationWrite, err)
	}
	path := temporary.Name()
	if err := temporary.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("%w: close descriptor staging: %v", ErrDestinationWrite, err)
	}
	defer os.Remove(path)
	descriptor := *parsed
	descriptor.Path = strings.TrimRight(descriptor.Path, "/") + "/repository.json"
	if _, err := c.fetch(ctx, descriptor.String(), path, 64<<10, "", "", nil, nil); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return validateRepositoryDescriptor(file)
}

func validateRepositoryRoot(root string) error {
	if err := filesystem.CheckOwnedDirectory(root); err != nil {
		return fmt.Errorf("invalid repository root: %w", err)
	}
	rootDirectory, err := openNoFollowDirectory(root)
	if err != nil {
		return fmt.Errorf("invalid repository root: %w", err)
	}
	defer rootDirectory.Close()
	entries, err := rootDirectory.ReadDir(-1)
	if err != nil {
		return err
	}
	seenDescriptor, seenV1 := false, false
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case name == "repository.json":
			seenDescriptor = true
			file, openErr := openNoFollowFileAt(rootDirectory, name)
			if openErr != nil {
				return fmt.Errorf("invalid repository descriptor: %w", openErr)
			}
			info, statErr := file.Stat()
			if statErr != nil || !info.Mode().IsRegular() || !singleLink(info) {
				file.Close()
				return errors.New("invalid repository descriptor: not a regular file")
			}
			decodeErr := validateRepositoryDescriptor(file)
			closeErr := file.Close()
			if decodeErr != nil {
				return decodeErr
			}
			if closeErr != nil {
				return closeErr
			}
		case name == "v1":
			seenV1 = true
			v1, openErr := openNoFollowDirectoryAt(rootDirectory, name)
			if openErr != nil {
				return fmt.Errorf("invalid repository directory %q: %w", name, openErr)
			}
			if err := validateRepositoryAlgorithms(v1); err != nil {
				v1.Close()
				return err
			}
			if closeErr := v1.Close(); closeErr != nil {
				return closeErr
			}
		case name == ".sync.lock":
			if err := validateRepositoryTransientFile(rootDirectory, name); err != nil {
				return err
			}
		case strings.HasPrefix(name, ".object-stage-") && len(name) > len(".object-stage-"):
			staging, openErr := openNoFollowDirectoryAt(rootDirectory, name)
			if openErr != nil {
				return fmt.Errorf("invalid repository staging entry %q: %w", name, openErr)
			}
			staging.Close()
		case strings.HasPrefix(name, ".descriptor-") && len(name) > len(".descriptor-"):
			if err := validateRepositoryTransientFile(rootDirectory, name); err != nil {
				return err
			}
		default:
			return fmt.Errorf("repository contains unexpected entry %q", name)
		}
	}
	if !seenDescriptor || !seenV1 {
		return errors.New("repository descriptor or v1 directory is missing")
	}
	return nil
}

func validateRepositoryTransientFile(parent *os.File, name string) error {
	file, err := openNoFollowFileAt(parent, name)
	if err != nil {
		return fmt.Errorf("invalid repository transient entry %q: %w", name, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !singleLink(info) {
		return fmt.Errorf("invalid repository transient entry %q", name)
	}
	return nil
}

func validateRepositoryAlgorithms(v1 *os.File) error {
	entries, err := v1.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() != "sha256" && entry.Name() != "sha512" {
			return fmt.Errorf("repository v1 contains unexpected entry %q", entry.Name())
		}
		directory, openErr := openNoFollowDirectoryAt(v1, entry.Name())
		if openErr != nil {
			return fmt.Errorf("invalid repository directory %q: %w", entry.Name(), openErr)
		}
		objects, readErr := directory.ReadDir(-1)
		if readErr != nil {
			directory.Close()
			return readErr
		}
		for _, object := range objects {
			if _, err := checksum.NewHasher(entry.Name(), object.Name()); err != nil {
				directory.Close()
				return fmt.Errorf("unsafe repository object %q: %w", object.Name(), err)
			}
			file, fileErr := openNoFollowFileAt(directory, object.Name())
			if fileErr != nil {
				directory.Close()
				return fmt.Errorf("unsafe repository object %q: %w", object.Name(), fileErr)
			}
			info, statErr := file.Stat()
			file.Close()
			if statErr != nil || !info.Mode().IsRegular() || !singleLink(info) {
				directory.Close()
				return fmt.Errorf("unsafe repository object %q", object.Name())
			}
		}
		if closeErr := directory.Close(); closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func validateRepositoryDescriptor(reader io.Reader) error {
	data, err := io.ReadAll(io.LimitReader(reader, maxRepositoryDescriptorBytes+1))
	if err != nil {
		return fmt.Errorf("invalid repository descriptor: %w", err)
	}
	if int64(len(data)) > maxRepositoryDescriptorBytes {
		return fmt.Errorf("repository descriptor exceeds %d bytes", maxRepositoryDescriptorBytes)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var descriptor repositoryDescriptor
	if err := decoder.Decode(&descriptor); err != nil {
		return fmt.Errorf("invalid repository descriptor: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("repository descriptor must contain one JSON object")
	}
	if descriptor.Format != "content-repository" || descriptor.Version != 1 {
		return fmt.Errorf("unsupported repository descriptor format/version %q/%d", descriptor.Format, descriptor.Version)
	}
	return nil
}

func copyLocalArtifact(ctx context.Context, sourceRoot, destination string, maxBytes int64, algorithm, expected string) (int64, error) {
	file, err := openRepositoryObject(sourceRoot, algorithm, expected)
	if err != nil {
		return 0, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !singleLink(opened) {
		return 0, errors.New("source object is not a regular file")
	}
	if opened.Size() > maxBytes {
		return 0, ErrTooLarge
	}
	if destination == "" || !filepath.IsAbs(destination) {
		return 0, errors.New("download destination must be absolute")
	}
	if err := filesystem.SecureMkdirAll(filepath.Dir(destination), 0700); err != nil {
		return 0, fmt.Errorf("%w: prepare destination: %v", ErrDestinationWrite, err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".tarlink-source-*")
	if err != nil {
		return 0, fmt.Errorf("%w: create temporary source: %v", ErrDestinationWrite, err)
	}
	p := temporary.Name()
	defer os.Remove(p)
	defer temporary.Close()
	h, err := newHasher(algorithm, expected)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(io.MultiWriter(destinationWriter{Writer: temporary}, h), io.LimitReader(contextReader{ctx: ctx, reader: file}, maxBytes+1))
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return 0, contextErr
		}
		return 0, err
	}
	if n > maxBytes {
		return 0, ErrTooLarge
	}
	if hex.EncodeToString(h.Sum(nil)) != expected {
		return 0, ErrChecksumMismatch
	}
	if err := temporary.Sync(); err != nil {
		return 0, fmt.Errorf("%w: flush temporary source: %v", ErrDestinationWrite, err)
	}
	if err := temporary.Close(); err != nil {
		return 0, fmt.Errorf("%w: close temporary source: %v", ErrDestinationWrite, err)
	}
	if _, ok := validCached(destination, algorithm, expected, maxBytes); ok {
		_ = os.Remove(p)
		return n, nil
	}
	if err := os.Rename(p, destination); err != nil {
		return 0, fmt.Errorf("%w: publish source: %v", ErrDestinationWrite, err)
	}
	if err := syncDirectory(filepath.Dir(destination)); err != nil {
		return 0, fmt.Errorf("%w: flush source directory: %v", ErrDestinationWrite, err)
	}
	return n, nil
}

type destinationWriter struct {
	io.Writer
}

func (w destinationWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	if err != nil {
		return n, fmt.Errorf("%w: write local artifact: %w", ErrDestinationWrite, err)
	}
	return n, nil
}

func openRepositoryObject(root, algorithm, digest string) (*os.File, error) {
	rootDirectory, err := openNoFollowDirectory(root)
	if err != nil {
		return nil, err
	}
	v1, err := openNoFollowDirectoryAt(rootDirectory, "v1")
	rootDirectory.Close()
	if err != nil {
		return nil, err
	}
	algorithmDirectory, err := openNoFollowDirectoryAt(v1, algorithm)
	v1.Close()
	if err != nil {
		return nil, err
	}
	fd, err := syscall.Openat(int(algorithmDirectory.Fd()), digest, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	algorithmDirectory.Close()
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), digest)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, errors.New("open repository object")
	}
	return file, nil
}

func openNoFollowDirectory(path string) (*os.File, error) {
	fd, err := openNoFollowAbsoluteDirectory(path)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, errors.New("open repository directory")
	}
	return file, nil
}

func openNoFollowAbsoluteDirectory(path string) (int, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return -1, errors.New("repository path must be absolute and clean")
	}
	fd, err := syscall.Open(string(filepath.Separator), syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		next, openErr := syscall.Openat(fd, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		_ = syscall.Close(fd)
		if openErr != nil {
			return -1, openErr
		}
		fd = next
	}
	return fd, nil
}

func openNoFollowDirectoryAt(parent *os.File, name string) (*os.File, error) {
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, errors.New("open repository directory")
	}
	return file, nil
}

func openNoFollowFileAt(parent *os.File, name string) (*os.File, error) {
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), name)
	if file == nil {
		_ = syscall.Close(fd)
		return nil, errors.New("open repository file")
	}
	return file, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

type responseBodyReader struct {
	ctx  context.Context
	body io.Reader
}

func (r responseBodyReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.body.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, fmt.Errorf("%w: read response body: %v", ErrNetwork, err)
	}
	return n, err
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

func (c *Client) FetchFile(ctx context.Context, request FileRequest) (Result, error) {
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
	req.Header.Set("User-Agent", userAgent())
	// Hash and size checks apply to the exact archived bytes, never an
	// automatically decoded HTTP content encoding.
	req.Header.Set("Accept-Encoding", "identity")
	resp, err := httpClient.Do(req)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return Result{}, contextErr
		}
		return Result{}, fmt.Errorf("%w: %w", ErrNetwork, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("%w: HTTP %d", ErrNetwork, resp.StatusCode)
	}
	if resp.ContentLength > maxBytes {
		return Result{}, ErrTooLarge
	}

	if err := filesystem.SecureMkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return Result{}, fmt.Errorf("%w: create download directory: %v", ErrDestinationWrite, err)
	}
	temporary, err := os.CreateTemp(filepath.Dir(destination), ".tarlink-download-*")
	if err != nil {
		return Result{}, fmt.Errorf("%w: create temporary download: %v", ErrDestinationWrite, err)
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
		return Result{}, fmt.Errorf("%w: secure temporary download: %v", ErrDestinationWrite, err)
	}

	limited := io.LimitReader(responseBodyReader{ctx: ctx, body: resp.Body}, maxBytes+1)
	writer := io.Writer(temporary)
	if hasher != nil {
		writer = io.MultiWriter(temporary, hasher)
	}
	written, err := copyWithProgress(writer, limited, resp.ContentLength, progress)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return Result{}, contextErr
		}
		if errors.Is(err, ErrNetwork) {
			return Result{}, err
		}
		return Result{}, fmt.Errorf("%w: write download: %v", ErrDestinationWrite, err)
	}
	if resp.ContentLength > 0 && written != resp.ContentLength {
		return Result{}, fmt.Errorf("%w: response body truncated: expected %d bytes, got %d", ErrNetwork, resp.ContentLength, written)
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
		return Result{}, fmt.Errorf("%w: flush download: %v", ErrDestinationWrite, err)
	}
	if err := temporary.Close(); err != nil {
		return Result{}, fmt.Errorf("%w: close download: %v", ErrDestinationWrite, err)
	}
	if existing, ok := validCached(destination, algorithm, expected, maxBytes); ok {
		existing.Cached = true
		return existing, nil
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return Result{}, fmt.Errorf("%w: publish download: %v", ErrDestinationWrite, err)
	}
	if err := syncDirectory(filepath.Dir(destination)); err != nil {
		return Result{}, fmt.Errorf("%w: flush download directory: %v", ErrDestinationWrite, err)
	}
	keep = true
	return Result{Path: destination, Algorithm: algorithm, Digest: digest, Bytes: written}, nil
}

func userAgent() string {
	current := strings.TrimSpace(version.Current)
	if current == "" {
		current = "development"
	}
	return "TarLink/" + current
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
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !singleLink(info) || info.Size() > maxBytes {
		return Result{}, false
	}
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return Result{}, false
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(info, openedInfo) || !openedInfo.Mode().IsRegular() || !singleLink(openedInfo) || openedInfo.Size() > maxBytes {
		return Result{}, false
	}
	postInfo, postErr := os.Lstat(path)
	if postErr != nil || !os.SameFile(postInfo, openedInfo) {
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
		return Result{}, false
	}
	return Result{Path: path, Algorithm: algorithm, Digest: digest, Bytes: written}, true
}

func validateDigest(algorithm, value string) error {
	_, err := newHasher(algorithm, value)
	return err
}

func newHasher(algorithm, value string) (hash.Hash, error) {
	return checksum.NewHasher(algorithm, value)
}

func singleLink(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return !ok || stat.Nlink == 1
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
