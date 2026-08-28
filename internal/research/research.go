// Package research provides advisory GitHub release research primitives.
// It never approves, installs, or mutates registry data.
package research

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/drobilica/tarlink/internal/appimage"
	"github.com/drobilica/tarlink/internal/archive"
	"github.com/drobilica/tarlink/internal/checksum"
	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/filesystem"
)

const DiscoveryTTL = 24 * time.Hour
const maxResponseBytes int64 = 4 << 20
const maxArtifactBytes int64 = 8 << 30
const MaxIconBytes int64 = 4 << 20
const maxArchiveIconBytes int64 = 1 << 20

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
var gitObjectPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)
var linuxAMD64Pattern = regexp.MustCompile(`(?i)(^|[^a-z0-9])linux[-_.]?(x64|amd64|x86[-_.]?64)([^a-z0-9]|$)`)
var linuxARM64Pattern = regexp.MustCompile(`(?i)(^|[^a-z0-9])linux[-_.]?(arm64|aarch64)([^a-z0-9]|$)`)
var linux64Pattern = regexp.MustCompile(`(?i)(^|[^a-z0-9])linux64([^a-z0-9]|$)`)

type Repository string

func ParseRepository(raw string) (Repository, error) {
	raw = strings.TrimSpace(raw)
	if strings.HasPrefix(raw, "https://github.com/") {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") || u.RawQuery != "" || u.Fragment != "" || u.User != nil || u.Opaque != "" {
			return "", fmt.Errorf("invalid GitHub repository %q", raw)
		}
		if strings.Contains(u.EscapedPath(), "%") || strings.HasPrefix(u.Path, "/") && (strings.HasSuffix(u.Path, "/") || strings.Contains(u.Path, "//")) {
			return "", fmt.Errorf("invalid GitHub repository %q", raw)
		}
		raw = strings.TrimPrefix(u.Path, "/")
	}
	parts := strings.Split(raw, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" || parts[0] == "." || parts[0] == ".." || parts[1] == "." || parts[1] == ".." || !repoPattern.MatchString(raw) || strings.Contains(raw, "//") {
		return "", fmt.Errorf("invalid GitHub repository %q", raw)
	}
	// GitHub repository names are case-insensitive. Lowercase is the one
	// canonical representation used for cache and evidence identity.
	return Repository(strings.ToLower(raw)), nil
}

// ReleaseAssetTarget identifies one exact GitHub release download URL. The
// URL is retained for display, while Repository, Tag, and Asset are the
// identity used to query and verify GitHub metadata.
type ReleaseAssetTarget struct {
	Repository Repository `json:"repository"`
	Tag        string     `json:"tag"`
	Asset      string     `json:"asset"`
	URL        string     `json:"url"`
}

// ParseReleaseAssetURL parses only canonical GitHub release download URLs.
// Release tags may contain slashes; the final path component is always the
// exact asset name.
func ParseReleaseAssetURL(raw string) (ReleaseAssetTarget, error) {
	original := strings.TrimSpace(raw)
	u, err := url.Parse(original)
	if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(original, "#") || u.Opaque != "" {
		return ReleaseAssetTarget{}, fmt.Errorf("invalid GitHub release asset URL %q", raw)
	}
	encoded := strings.TrimPrefix(u.EscapedPath(), "/")
	if encoded == "" || strings.HasSuffix(encoded, "/") {
		return ReleaseAssetTarget{}, fmt.Errorf("invalid GitHub release asset URL %q", raw)
	}
	encodedParts := strings.Split(encoded, "/")
	if len(encodedParts) < 6 {
		return ReleaseAssetTarget{}, fmt.Errorf("invalid GitHub release asset URL %q", raw)
	}
	parts := make([]string, len(encodedParts))
	for i, encodedPart := range encodedParts {
		part, unescapeErr := url.PathUnescape(encodedPart)
		if unescapeErr != nil || part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\\x00\r\n") {
			return ReleaseAssetTarget{}, fmt.Errorf("invalid GitHub release asset URL %q", raw)
		}
		parts[i] = part
	}
	if !strings.EqualFold(parts[2], "releases") || !strings.EqualFold(parts[3], "download") {
		return ReleaseAssetTarget{}, fmt.Errorf("invalid GitHub release asset URL %q", raw)
	}
	repo, err := ParseRepository(strings.Join(parts[:2], "/"))
	if err != nil {
		return ReleaseAssetTarget{}, fmt.Errorf("invalid GitHub release asset URL %q", raw)
	}
	tag := strings.Join(parts[4:len(parts)-1], "/")
	asset := parts[len(parts)-1]
	if tag == "" || asset == "" || strings.ContainsAny(tag, "?#\x00\r\n") || strings.ContainsAny(asset, "/\\\x00\r\n") {
		return ReleaseAssetTarget{}, fmt.Errorf("invalid GitHub release asset URL %q", raw)
	}
	return ReleaseAssetTarget{Repository: repo, Tag: tag, Asset: asset, URL: original}, nil
}

// InferPlatform returns a platform only when the asset name contains a
// strong, explicit Linux architecture marker. Generic x64/arm64 names are
// intentionally left unresolved.
func InferPlatform(assetName string) string {
	name := strings.TrimSpace(assetName)
	if linuxAMD64Pattern.MatchString(name) {
		return "linux-amd64"
	}
	if linuxARM64Pattern.MatchString(name) {
		return "linux-arm64"
	}
	// linux64 is a conventional shorthand for Linux amd64, but only accept
	// it as a complete token to avoid interpreting unrelated names.
	if linux64Pattern.MatchString(name) {
		return "linux-amd64"
	}
	return ""
}

// TargetArchitecture derives the expected target architecture of a release
// asset from artifact-name evidence only. It returns "amd64", "arm64", or ""
// when the name does not encode an architecture. An empty result is
// ambiguity to be handled explicitly, never a reason to fall back to the
// host architecture.
func TargetArchitecture(assetName string) string {
	switch InferPlatform(assetName) {
	case "linux-amd64":
		return "amd64"
	case "linux-arm64":
		return "arm64"
	}
	return ""
}

type Release struct {
	ID          int64      `json:"id"`
	Repository  Repository `json:"repository"`
	Tag         string     `json:"tag_name"`
	Draft       bool       `json:"draft"`
	Prerelease  bool       `json:"prerelease"`
	CreatedAt   time.Time  `json:"created_at"`
	PublishedAt time.Time  `json:"published_at"`
	Assets      []Asset    `json:"assets"`
}

type Asset struct {
	ID          int64      `json:"id"`
	ReleaseID   int64      `json:"release_id"`
	Repository  Repository `json:"repository"`
	Name        string     `json:"name"`
	URL         string     `json:"browser_download_url"`
	Size        int64      `json:"size"`
	Digest      string     `json:"digest,omitempty"`
	Algorithm   string     `json:"algorithm,omitempty"`
	ContentType string     `json:"content_type,omitempty"`
	State       string     `json:"state"`
}

type Client struct {
	HTTP      *http.Client
	CacheRoot string
	Now       func() time.Time
	Refresh   bool
	TempRoot  string
	// APIBase is test-only/configuration injection; production leaves it empty.
	APIBase string
}
type APIError struct {
	Status  int
	Kind    string
	Message string
	Cause   error
}

var ErrCacheCorrupt = errors.New("verified artifact cache is corrupt")

const (
	APIErrorNotFound    = "not_found"
	APIErrorAuth        = "authentication_failure"
	APIErrorRateLimited = "rate_limited"
	APIErrorServer      = "server_failure"
	APIErrorMalformed   = "malformed_response"
	APIErrorRedirect    = "redirect_security_violation"
	APIErrorNetwork     = "network_failure"
)

func (e *APIError) Error() string { return e.Message }
func (e *APIError) Unwrap() error { return e.Cause }

func (c *Client) now() time.Time {
	if c != nil && c.Now != nil {
		return c.Now()
	}
	return time.Now()
}
func (c *Client) httpClient() *http.Client {
	if c != nil && c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

func APIURL(repo Repository) string {
	return "https://api.github.com/repos/" + string(repo) + "/releases"
}
func (c *Client) apiURL(repo Repository) string {
	if c != nil && c.APIBase != "" {
		return strings.TrimRight(c.APIBase, "/") + "/repos/" + string(repo) + "/releases"
	}
	return APIURL(repo)
}

func (c *Client) repositoryAPIURL(repo Repository) string {
	if c != nil && c.APIBase != "" {
		return strings.TrimRight(c.APIBase, "/") + "/repos/" + string(repo)
	}
	return "https://api.github.com/repos/" + string(repo)
}

func (c *Client) apiGet(ctx context.Context, endpoint string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, &APIError{Kind: APIErrorNetwork, Message: err.Error()}
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "tarlink-registry-research")
	// Maintainer tooling may use the conventional Actions credential to avoid
	// unauthenticated API limits. Never persist, print, or include it in an
	// error; runtime installation does not use this client.
	if token := strings.TrimSpace(os.Getenv("GH_TOKEN")); token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := *c.httpClient()
	origin, _ := url.Parse(endpoint)
	client.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return &APIError{Kind: APIErrorRedirect, Message: "GitHub API redirect limit exceeded"}
		}
		if r.URL.Scheme != origin.Scheme || !strings.EqualFold(r.URL.Host, origin.Host) {
			return &APIError{Kind: APIErrorRedirect, Message: "GitHub API cross-origin redirect rejected"}
		}
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		var ae *APIError
		if errors.As(err, &ae) {
			return nil, err
		}
		return nil, &APIError{Kind: APIErrorNetwork, Message: fmt.Sprintf("GitHub API request: %v", err), Cause: err}
	}
	return resp, nil
}

type apiRelease struct {
	ID          int64     `json:"id"`
	Tag         string    `json:"tag_name"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	CreatedAt   time.Time `json:"created_at"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		URL         string `json:"browser_download_url"`
		Size        int64  `json:"size"`
		Digest      string `json:"digest"`
		ContentType string `json:"content_type"`
		State       string `json:"state"`
	} `json:"assets"`
}

type discoveryFile struct {
	Version   int       `json:"version"`
	FetchedAt time.Time `json:"fetched_at"`
	Releases  []Release `json:"releases"`
}

func (c *Client) Discover(ctx context.Context, raw string) ([]Release, error) {
	repo, err := ParseRepository(raw)
	if err != nil {
		return nil, err
	}
	cache := ""
	if c != nil && c.CacheRoot != "" {
		cache = filepath.Join(c.CacheRoot, "discovery", cacheName(string(repo))+".json")
	}
	if c != nil && !c.Refresh && cache != "" {
		if f, e := os.Open(cache); e == nil {
			st, se := f.Stat()
			ls, le := os.Lstat(cache)
			if se != nil || le != nil || !st.Mode().IsRegular() || !ls.Mode().IsRegular() || ls.Mode()&os.ModeSymlink != 0 || !os.SameFile(st, ls) || st.Size() > maxResponseBytes {
				_ = f.Close()
				goto refresh
			}
			var d discoveryFile
			dec := json.NewDecoder(io.LimitReader(f, maxResponseBytes+1))
			dec.DisallowUnknownFields()
			e = dec.Decode(&d)
			if e == nil {
				var extra any
				e = dec.Decode(&extra)
				if e != io.EOF {
					e = errors.New("trailing discovery cache data")
				} else {
					e = nil
				}
			}
			_ = f.Close()
			if e == nil && validDiscovery(repo, d) && !d.FetchedAt.After(c.now()) && c.now().Sub(d.FetchedAt) < DiscoveryTTL {
				return d.Releases, nil
			}
		}
	}
refresh:
	resp, err := c.apiGet(ctx, c.apiURL(repo)+"?per_page=100")
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			return nil, err
		}
		return nil, &APIError{Kind: APIErrorNetwork, Message: fmt.Sprintf("GitHub releases request: %v", err)}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, classifyStatus(resp)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, &APIError{Kind: APIErrorNetwork, Message: fmt.Sprintf("read GitHub releases: %v", err), Cause: err}
	}
	if int64(len(body)) > maxResponseBytes {
		return nil, &APIError{Kind: APIErrorMalformed, Message: "GitHub releases response exceeds size limit"}
	}
	var rawReleases []apiRelease
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&rawReleases); err != nil {
		return nil, &APIError{Kind: APIErrorMalformed, Message: fmt.Sprintf("decode GitHub releases: %v", err)}
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, &APIError{Kind: APIErrorMalformed, Message: "trailing GitHub releases response data"}
	}
	result := make([]Release, 0, len(rawReleases))
	for _, in := range rawReleases {
		if in.ID <= 0 || strings.TrimSpace(in.Tag) == "" {
			return nil, &APIError{Kind: APIErrorMalformed, Message: "malformed GitHub release metadata"}
		}
		r := Release{ID: in.ID, Repository: repo, Tag: in.Tag, Draft: in.Draft, Prerelease: in.Prerelease, CreatedAt: in.CreatedAt, PublishedAt: in.PublishedAt}
		seen := make(map[string]bool)
		seenIDs := make(map[int64]bool)
		for _, a := range in.Assets {
			if a.ID <= 0 || a.Name == "" || seen[a.Name] || seenIDs[a.ID] || a.Size < 0 || !validDownloadURL(a.URL) {
				return nil, &APIError{Kind: APIErrorMalformed, Message: "malformed GitHub asset metadata"}
			}
			seen[a.Name] = true
			seenIDs[a.ID] = true
			if a.State != "uploaded" {
				return nil, &APIError{Kind: APIErrorMalformed, Message: "GitHub asset is not fully uploaded"}
			}
			r.Assets = append(r.Assets, Asset{ID: a.ID, ReleaseID: in.ID, Repository: repo, Name: a.Name, URL: a.URL, Size: a.Size, Digest: a.Digest, ContentType: a.ContentType, State: a.State})
		}
		result = append(result, r)
	}
	seenIDs := make(map[int64]bool)
	seenTags := make(map[string]bool)
	for _, r := range result {
		if seenIDs[r.ID] || seenTags[r.Tag] || r.Repository != repo {
			return nil, &APIError{Kind: APIErrorMalformed, Message: "duplicate or inconsistent GitHub release metadata"}
		}
		seenIDs[r.ID], seenTags[r.Tag] = true, true
	}
	if cache != "" {
		if err := writeJSON(cache, discoveryFile{Version: 1, FetchedAt: c.now(), Releases: result}); err != nil {
			return result, fmt.Errorf("cache GitHub discovery: %w", err)
		}
	}
	return result, nil
}

// DiscoverRelease performs GitHub's exact release-by-tag lookup, avoiding
// assumptions about the first releases page.
func (c *Client) DiscoverRelease(ctx context.Context, raw, tag string) (Release, error) {
	repo, err := ParseRepository(raw)
	if err != nil {
		return Release{}, err
	}
	if tag == "" || strings.ContainsAny(tag, "?#") {
		return Release{}, &APIError{Kind: APIErrorMalformed, Message: "invalid release tag"}
	}
	cache := ""
	if c != nil && c.CacheRoot != "" {
		cache = filepath.Join(c.CacheRoot, "discovery", "tag-"+cacheName(string(repo)+"\x00"+tag)+".json")
	}
	if c != nil && !c.Refresh && cache != "" {
		if f, e := os.Open(cache); e == nil {
			st, se := f.Stat()
			ls, le := os.Lstat(cache)
			if se == nil && le == nil && st.Mode().IsRegular() && ls.Mode().IsRegular() && ls.Mode()&os.ModeSymlink == 0 && os.SameFile(st, ls) && st.Size() <= maxResponseBytes {
				var d discoveryFile
				dec := json.NewDecoder(io.LimitReader(f, maxResponseBytes+1))
				dec.DisallowUnknownFields()
				de := dec.Decode(&d)
				var extra any
				if de == nil {
					if e2 := dec.Decode(&extra); e2 == io.EOF {
						de = nil
					} else {
						de = errors.New("trailing discovery cache data")
					}
				}
				_ = f.Close()
				if de == nil && validDiscovery(repo, d) && len(d.Releases) == 1 && d.Releases[0].Tag == tag && !d.FetchedAt.After(c.now()) && c.now().Sub(d.FetchedAt) < DiscoveryTTL {
					return d.Releases[0], nil
				}
			} else {
				_ = f.Close()
			}
		}
	}
	endpoint := strings.TrimRight(c.apiURL(repo), "/") + "/tags/" + url.PathEscape(tag)
	resp, err := c.apiGet(ctx, endpoint)
	if err != nil {
		return Release{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, classifyStatus(resp)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return Release{}, &APIError{Kind: APIErrorNetwork, Message: err.Error(), Cause: err}
	}
	if int64(len(body)) > maxResponseBytes {
		return Release{}, &APIError{Kind: APIErrorMalformed, Message: "GitHub release response exceeds size limit"}
	}
	var in apiRelease
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&in); err != nil {
		return Release{}, &APIError{Kind: APIErrorMalformed, Message: err.Error()}
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return Release{}, &APIError{Kind: APIErrorMalformed, Message: "trailing GitHub release response data"}
	}
	if in.ID <= 0 || in.Tag == "" {
		return Release{}, &APIError{Kind: APIErrorMalformed, Message: "malformed GitHub release metadata"}
	}
	if in.Tag != tag {
		return Release{}, &APIError{Kind: APIErrorMalformed, Message: "GitHub release tag identity mismatch"}
	}
	r := Release{ID: in.ID, Repository: repo, Tag: in.Tag, Draft: in.Draft, Prerelease: in.Prerelease, CreatedAt: in.CreatedAt, PublishedAt: in.PublishedAt}
	if r.Repository != repo {
		return Release{}, &APIError{Kind: APIErrorMalformed, Message: "release repository identity mismatch"}
	}
	seen := map[int64]bool{}
	seenNames := map[string]bool{}
	for _, a := range in.Assets {
		if a.ID <= 0 || seen[a.ID] || seenNames[a.Name] || a.Name == "" || a.Size < 0 || !validDownloadURL(a.URL) || a.State != "uploaded" {
			return Release{}, &APIError{Kind: APIErrorMalformed, Message: "malformed or incomplete GitHub asset metadata"}
		}
		seen[a.ID] = true
		seenNames[a.Name] = true
		r.Assets = append(r.Assets, Asset{ID: a.ID, ReleaseID: in.ID, Repository: repo, Name: a.Name, URL: a.URL, Size: a.Size, Digest: a.Digest, ContentType: a.ContentType, State: a.State})
	}
	if cache != "" {
		_ = writeJSON(cache, discoveryFile{Version: 1, FetchedAt: c.now(), Releases: []Release{r}})
	}
	return r, nil
}

// ResolvedReleaseAsset is the verified association between a direct download
// target and the release/asset metadata returned by GitHub.
type ResolvedReleaseAsset struct {
	Repository Repository `json:"repository"`
	Release    Release    `json:"release"`
	Asset      Asset      `json:"asset"`
}

// ResolveReleaseAsset performs an exact release-by-tag lookup and selects the
// exact named asset. It never guesses an asset from a release listing.
func (c *Client) ResolveReleaseAsset(ctx context.Context, target ReleaseAssetTarget) (ResolvedReleaseAsset, error) {
	repo, err := ParseRepository(string(target.Repository))
	if err != nil || repo != target.Repository || target.Tag == "" || target.Asset == "" {
		return ResolvedReleaseAsset{}, &APIError{Kind: APIErrorMalformed, Message: "invalid release asset target"}
	}
	release, err := c.DiscoverRelease(ctx, string(repo), target.Tag)
	if err != nil {
		return ResolvedReleaseAsset{}, err
	}
	if release.Repository != repo || release.Tag != target.Tag {
		return ResolvedReleaseAsset{}, &APIError{Kind: APIErrorMalformed, Message: "GitHub release identity mismatch"}
	}
	var selected Asset
	for _, asset := range release.Assets {
		if asset.Name != target.Asset {
			continue
		}
		if selected.ID != 0 {
			return ResolvedReleaseAsset{}, &APIError{Kind: APIErrorMalformed, Message: "duplicate GitHub asset identity"}
		}
		selected = asset
	}
	if selected.ID == 0 {
		return ResolvedReleaseAsset{}, &APIError{Kind: APIErrorNotFound, Message: fmt.Sprintf("GitHub release asset %q was not found", target.Asset)}
	}
	if selected.Repository != repo || selected.ReleaseID != release.ID || selected.Name != target.Asset || selected.State != "uploaded" || !validDownloadURL(selected.URL) {
		return ResolvedReleaseAsset{}, &APIError{Kind: APIErrorMalformed, Message: "GitHub asset identity mismatch"}
	}
	return ResolvedReleaseAsset{Repository: repo, Release: release, Asset: selected}, nil
}

// RepositoryFile identifies one immutable regular file in a GitHub
// repository. Commit is the exact commit selected by a release tag; URL is
// therefore immutable even if the tag is later moved.
type RepositoryFile struct {
	Repository Repository `json:"repository"`
	Tag        string     `json:"tag"`
	Commit     string     `json:"commit"`
	Path       string     `json:"path"`
	Blob       string     `json:"blob"`
	Size       int64      `json:"size"`
	URL        string     `json:"url"`
}

type apiGitObject struct {
	SHA  string `json:"sha"`
	Type string `json:"type"`
}

type apiGitReference struct {
	Ref    string       `json:"ref"`
	Object apiGitObject `json:"object"`
}

type apiGitTag struct {
	SHA    string       `json:"sha"`
	Object apiGitObject `json:"object"`
}

type apiGitCommit struct {
	SHA  string `json:"sha"`
	Tree struct {
		SHA string `json:"sha"`
	} `json:"tree"`
}

type apiGitTree struct {
	SHA       string `json:"sha"`
	Truncated bool   `json:"truncated"`
	Tree      []struct {
		Path string `json:"path"`
		Type string `json:"type"`
		SHA  string `json:"sha"`
		Size *int64 `json:"size,omitempty"`
	} `json:"tree"`
}

// DiscoverRepositoryIconCandidates resolves an exact GitHub tag and returns
// the small set of repository-tree paths that strongly resemble application
// icons. The recursive tree is accepted only when GitHub returns it in full.
func (c *Client) DiscoverRepositoryIconCandidates(ctx context.Context, raw, tag string) ([]RepositoryFile, error) {
	repo, commitSHA, treeSHA, base, err := c.resolveRepositoryCommit(ctx, raw, tag)
	if err != nil {
		return nil, err
	}
	var tree apiGitTree
	if err := c.getAPIJSON(ctx, base+"/git/trees/"+treeSHA+"?recursive=1", &tree); err != nil {
		return nil, err
	}
	if tree.SHA != treeSHA || tree.Truncated {
		return nil, &APIError{Kind: APIErrorMalformed, Message: "GitHub tree response is truncated or inconsistent"}
	}
	candidates := make([]RepositoryFile, 0)
	for _, entry := range tree.Tree {
		if entry.Type != "blob" || !gitObjectPattern.MatchString(entry.SHA) || entry.Size == nil || *entry.Size < 0 || !looksLikeIconPath(entry.Path) {
			continue
		}
		parts, pathErr := repositoryPathParts(entry.Path)
		if pathErr != nil {
			continue
		}
		escaped := make([]string, len(parts))
		for i, part := range parts {
			escaped[i] = url.PathEscape(part)
		}
		candidates = append(candidates, RepositoryFile{
			Repository: repo, Tag: tag, Commit: commitSHA, Path: strings.Join(parts, "/"), Blob: entry.SHA, Size: *entry.Size,
			URL: "https://raw.githubusercontent.com/" + string(repo) + "/" + commitSHA + "/" + strings.Join(escaped, "/"),
		})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Path < candidates[j].Path })
	return candidates, nil
}

func looksLikeIconPath(value string) bool {
	if !strings.EqualFold(path.Ext(value), ".png") {
		return false
	}
	parts := strings.Split(value, "/")
	base := strings.ToLower(parts[len(parts)-1])
	if base == "icon.png" || base == "logo.png" {
		return true
	}
	for _, part := range parts[:len(parts)-1] {
		if strings.EqualFold(part, "icon") || strings.EqualFold(part, "icons") {
			return true
		}
	}
	return false
}

func (c *Client) resolveRepositoryCommit(ctx context.Context, raw, tag string) (Repository, string, string, string, error) {
	repo, err := ParseRepository(raw)
	if err != nil {
		return "", "", "", "", err
	}
	if tag == "" || strings.ContainsAny(tag, "?#") {
		return "", "", "", "", &APIError{Kind: APIErrorMalformed, Message: "invalid release tag"}
	}
	base := c.repositoryAPIURL(repo)
	var reference apiGitReference
	if err := c.getAPIJSON(ctx, base+"/git/ref/tags/"+url.PathEscape(tag), &reference); err != nil {
		return "", "", "", "", err
	}
	if reference.Ref != "refs/tags/"+tag || !validGitObject(reference.Object) {
		return "", "", "", "", &APIError{Kind: APIErrorMalformed, Message: "GitHub tag reference identity mismatch"}
	}
	object := reference.Object
	seen := make(map[string]bool)
	for depth := 0; object.Type == "tag"; depth++ {
		if depth >= 8 || seen[object.SHA] {
			return "", "", "", "", &APIError{Kind: APIErrorMalformed, Message: "GitHub annotated tag chain is invalid"}
		}
		seen[object.SHA] = true
		var annotated apiGitTag
		if err := c.getAPIJSON(ctx, base+"/git/tags/"+object.SHA, &annotated); err != nil {
			return "", "", "", "", err
		}
		if annotated.SHA != object.SHA || !validGitObject(annotated.Object) {
			return "", "", "", "", &APIError{Kind: APIErrorMalformed, Message: "GitHub annotated tag identity mismatch"}
		}
		object = annotated.Object
	}
	if object.Type != "commit" {
		return "", "", "", "", &APIError{Kind: APIErrorMalformed, Message: "GitHub tag does not resolve to a commit"}
	}
	var commit apiGitCommit
	if err := c.getAPIJSON(ctx, base+"/git/commits/"+object.SHA, &commit); err != nil {
		return "", "", "", "", err
	}
	if commit.SHA != object.SHA || !gitObjectPattern.MatchString(commit.Tree.SHA) {
		return "", "", "", "", &APIError{Kind: APIErrorMalformed, Message: "GitHub commit identity mismatch"}
	}
	return repo, object.SHA, commit.Tree.SHA, base, nil
}

// DiscoverRepositoryFile resolves an exact GitHub tag, dereferences annotated
// tags, and walks non-recursive Git trees one path component at a time. This
// avoids ambiguous branch/tag resolution and GitHub's truncated recursive-tree
// responses while retaining strict response bounds.
func (c *Client) DiscoverRepositoryFile(ctx context.Context, raw, tag, filePath string) (RepositoryFile, error) {
	repo, err := ParseRepository(raw)
	if err != nil {
		return RepositoryFile{}, err
	}
	if tag == "" || strings.ContainsAny(tag, "?#") {
		return RepositoryFile{}, &APIError{Kind: APIErrorMalformed, Message: "invalid release tag"}
	}
	parts, err := repositoryPathParts(filePath)
	if err != nil {
		return RepositoryFile{}, err
	}
	base := c.repositoryAPIURL(repo)
	var reference apiGitReference
	if err := c.getAPIJSON(ctx, base+"/git/ref/tags/"+url.PathEscape(tag), &reference); err != nil {
		return RepositoryFile{}, err
	}
	if reference.Ref != "refs/tags/"+tag || !validGitObject(reference.Object) {
		return RepositoryFile{}, &APIError{Kind: APIErrorMalformed, Message: "GitHub tag reference identity mismatch"}
	}
	object := reference.Object
	seen := make(map[string]bool)
	for depth := 0; object.Type == "tag"; depth++ {
		if depth >= 8 || seen[object.SHA] {
			return RepositoryFile{}, &APIError{Kind: APIErrorMalformed, Message: "GitHub annotated tag chain is invalid"}
		}
		seen[object.SHA] = true
		var annotated apiGitTag
		if err := c.getAPIJSON(ctx, base+"/git/tags/"+object.SHA, &annotated); err != nil {
			return RepositoryFile{}, err
		}
		if annotated.SHA != object.SHA || !validGitObject(annotated.Object) {
			return RepositoryFile{}, &APIError{Kind: APIErrorMalformed, Message: "GitHub annotated tag identity mismatch"}
		}
		object = annotated.Object
	}
	if object.Type != "commit" {
		return RepositoryFile{}, &APIError{Kind: APIErrorMalformed, Message: "GitHub tag does not resolve to a commit"}
	}
	commitSHA := object.SHA
	var commit apiGitCommit
	if err := c.getAPIJSON(ctx, base+"/git/commits/"+commitSHA, &commit); err != nil {
		return RepositoryFile{}, err
	}
	if commit.SHA != commitSHA || !gitObjectPattern.MatchString(commit.Tree.SHA) {
		return RepositoryFile{}, &APIError{Kind: APIErrorMalformed, Message: "GitHub commit identity mismatch"}
	}
	treeSHA := commit.Tree.SHA
	var blobSHA string
	var size int64
	for index, part := range parts {
		var tree apiGitTree
		if err := c.getAPIJSON(ctx, base+"/git/trees/"+treeSHA, &tree); err != nil {
			return RepositoryFile{}, err
		}
		if tree.SHA != treeSHA || tree.Truncated {
			return RepositoryFile{}, &APIError{Kind: APIErrorMalformed, Message: "GitHub tree response is truncated or inconsistent"}
		}
		var selected *struct {
			Path string `json:"path"`
			Type string `json:"type"`
			SHA  string `json:"sha"`
			Size *int64 `json:"size,omitempty"`
		}
		for entryIndex := range tree.Tree {
			entry := &tree.Tree[entryIndex]
			if entry.Path == part {
				if selected != nil {
					return RepositoryFile{}, &APIError{Kind: APIErrorMalformed, Message: "GitHub tree contains duplicate paths"}
				}
				selected = entry
			}
		}
		if selected == nil || !gitObjectPattern.MatchString(selected.SHA) {
			return RepositoryFile{}, &APIError{Kind: APIErrorNotFound, Message: fmt.Sprintf("repository file %q was not found at tag %q", filePath, tag)}
		}
		last := index == len(parts)-1
		if !last {
			if selected.Type != "tree" {
				return RepositoryFile{}, &APIError{Kind: APIErrorNotFound, Message: fmt.Sprintf("repository path %q is not a directory", strings.Join(parts[:index+1], "/"))}
			}
			treeSHA = selected.SHA
			continue
		}
		if selected.Type != "blob" || selected.Size == nil || *selected.Size < 0 {
			return RepositoryFile{}, &APIError{Kind: APIErrorMalformed, Message: "repository icon is not a regular Git blob"}
		}
		blobSHA, size = selected.SHA, *selected.Size
	}
	escaped := make([]string, len(parts))
	for i, part := range parts {
		escaped[i] = url.PathEscape(part)
	}
	rawURL := "https://raw.githubusercontent.com/" + string(repo) + "/" + commitSHA + "/" + strings.Join(escaped, "/")
	return RepositoryFile{Repository: repo, Tag: tag, Commit: commitSHA, Path: strings.Join(parts, "/"), Blob: blobSHA, Size: size, URL: rawURL}, nil
}

func repositoryPathParts(value string) ([]string, error) {
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || strings.ContainsAny(value, "\x00\r\n") || !strings.EqualFold(path.Ext(value), ".png") {
		return nil, fmt.Errorf("invalid repository PNG path %q", value)
	}
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, fmt.Errorf("invalid repository PNG path %q", value)
		}
	}
	return parts, nil
}

func validGitObject(object apiGitObject) bool {
	return gitObjectPattern.MatchString(object.SHA) && (object.Type == "commit" || object.Type == "tag")
}

func (c *Client) getAPIJSON(ctx context.Context, endpoint string, destination any) error {
	response, err := c.apiGet(ctx, endpoint)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return classifyStatus(response)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return &APIError{Kind: APIErrorNetwork, Message: fmt.Sprintf("read GitHub API response: %v", err), Cause: err}
	}
	if int64(len(body)) > maxResponseBytes {
		return &APIError{Kind: APIErrorMalformed, Message: "GitHub API response exceeds size limit"}
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	if err := decoder.Decode(destination); err != nil {
		return &APIError{Kind: APIErrorMalformed, Message: fmt.Sprintf("decode GitHub API response: %v", err)}
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return &APIError{Kind: APIErrorMalformed, Message: "trailing GitHub API response data"}
	}
	return nil
}

// FetchRepositoryFile downloads one previously discovered immutable GitHub
// blob through TarLink's bounded download client. The returned bytes are still
// untrusted; callers must validate the expected format before recording them.
func (c *Client) FetchRepositoryFile(ctx context.Context, file RepositoryFile) ([]byte, error) {
	parts, err := repositoryPathParts(file.Path)
	if err != nil {
		return nil, err
	}
	repo, err := ParseRepository(string(file.Repository))
	if err != nil || repo != file.Repository || !gitObjectPattern.MatchString(file.Commit) || !gitObjectPattern.MatchString(file.Blob) || file.Size < 0 || file.Size > MaxIconBytes {
		return nil, errors.New("invalid immutable repository file identity")
	}
	escaped := make([]string, len(parts))
	for i, part := range parts {
		escaped[i] = url.PathEscape(part)
	}
	expectedURL := "https://raw.githubusercontent.com/" + string(repo) + "/" + file.Commit + "/" + strings.Join(escaped, "/")
	if file.URL != expectedURL {
		return nil, errors.New("immutable repository file URL does not match its identity")
	}
	directory, err := os.MkdirTemp(func() string {
		if c != nil {
			return c.TempRoot
		}
		return ""
	}(), "tarlink-icon-research-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(directory)
	destination := filepath.Join(directory, "icon.png")
	client := download.NewClient()
	if c != nil && c.HTTP != nil {
		client.HTTP = c.HTTP
	}
	allowed := func(candidate *url.URL) bool {
		return candidate != nil && candidate.Scheme == "https" && candidate.User == nil && strings.EqualFold(candidate.Host, "raw.githubusercontent.com") && candidate.EscapedPath() == func() string {
			parsed, _ := url.Parse(expectedURL)
			return parsed.EscapedPath()
		}() && candidate.RawQuery == "" && candidate.Fragment == ""
	}
	result, err := client.FetchFile(ctx, download.FileRequest{URL: expectedURL, Destination: destination, MaxBytes: MaxIconBytes, AllowedURL: allowed})
	if err != nil {
		return nil, err
	}
	if result.Bytes != file.Size {
		return nil, fmt.Errorf("repository file size mismatch: expected %d, got %d", file.Size, result.Bytes)
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func classifyStatus(resp *http.Response) error {
	kind := APIErrorServer
	switch resp.StatusCode {
	case 404:
		kind = APIErrorNotFound
	case 401:
		kind = APIErrorAuth
	case 403:
		if resp.Header.Get("X-RateLimit-Remaining") == "0" || resp.Header.Get("Retry-After") != "" {
			kind = APIErrorRateLimited
		} else {
			kind = APIErrorAuth
		}
	case 429:
		kind = APIErrorRateLimited
	}
	return &APIError{Status: resp.StatusCode, Kind: kind, Message: fmt.Sprintf("GitHub API returned %s", resp.Status)}
}
func validDownloadURL(raw string) bool {
	u, e := url.Parse(raw)
	return e == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Fragment == ""
}
func validDiscovery(repo Repository, d discoveryFile) bool {
	if d.Version != 1 || d.FetchedAt.IsZero() || d.FetchedAt.Location() == nil {
		return false
	}
	seenReleases := make(map[int64]bool)
	seenTags := make(map[string]bool)
	for _, r := range d.Releases {
		if r.ID <= 0 || seenReleases[r.ID] || seenTags[r.Tag] || r.Repository != repo || r.Tag == "" {
			return false
		}
		seenReleases[r.ID], seenTags[r.Tag] = true, true
		seen := make(map[int64]bool)
		seenNames := make(map[string]bool)
		for _, a := range r.Assets {
			if a.ID <= 0 || seen[a.ID] || seenNames[a.Name] || a.ReleaseID != r.ID || a.Repository != repo || a.Name == "" || a.Size < 0 || !validDownloadURL(a.URL) || a.State != "uploaded" {
				return false
			}
			seen[a.ID] = true
			seenNames[a.Name] = true
		}
	}
	return true
}

func cacheName(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func writeJSON(path string, value any) error {
	if err := filesystem.SecureMkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".research-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if err = json.NewEncoder(f).Encode(value); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Chmod(0600); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

type Verdict string

const (
	Acceptable Verdict = "ACCEPTABLE"
	Rejected   Verdict = "REJECTED"
	Error      Verdict = "ERROR"
)

type Provenance struct {
	Verdict    Verdict    `json:"verdict"`
	Repository Repository `json:"repository,omitempty"`
	ReleaseID  int64      `json:"release_id,omitempty"`
	AssetID    int64      `json:"asset_id,omitempty"`
	AssetState string     `json:"asset_state,omitempty"`
	AssetSize  int64      `json:"asset_size,omitempty"`
	Algorithm  string     `json:"algorithm,omitempty"`
	Digest     string     `json:"digest,omitempty"`
	ReasonCode string     `json:"reason_code,omitempty"`
	Message    string     `json:"message"`
}

func EvaluateProvenance(repo Repository, release Release, asset Asset) Provenance {
	base := Provenance{Repository: repo, ReleaseID: release.ID, AssetID: asset.ID, AssetState: asset.State, AssetSize: asset.Size}
	if release.ID == 0 || release.Repository != repo || asset.ID == 0 || asset.ReleaseID != release.ID || asset.Repository != repo {
		return Provenance{Verdict: Rejected, ReasonCode: "ASSET_IDENTITY_CHANGED", Message: "asset is not associated with the selected release"}
	}
	if asset.State != "uploaded" {
		base.Verdict = Rejected
		base.ReasonCode = "ASSET_NOT_AVAILABLE"
		base.Message = "GitHub asset is not fully uploaded"
		return base
	}
	if strings.TrimSpace(asset.Digest) == "" {
		base.Verdict = Rejected
		base.ReasonCode = "NO_AUTHORITATIVE_DIGEST"
		base.Message = "GitHub did not publish an authoritative asset digest"
		return base
	}
	parts := strings.SplitN(asset.Digest, ":", 2)
	if len(parts) != 2 {
		base.Verdict = Rejected
		base.ReasonCode = "MALFORMED_DIGEST"
		base.Message = "GitHub asset digest has no supported algorithm prefix"
		return base
	}
	alg := parts[0]
	digest := parts[1]
	if alg != "sha256" && alg != "sha512" {
		base.Verdict = Rejected
		base.Algorithm = alg
		base.Digest = digest
		base.ReasonCode = "UNSUPPORTED_DIGEST_ALGORITHM"
		base.Message = "GitHub asset digest algorithm is unsupported"
		return base
	}
	if err := checksum.Validate(alg, digest); err != nil {
		base.Verdict = Rejected
		base.Algorithm = alg
		base.Digest = digest
		base.ReasonCode = "MALFORMED_DIGEST"
		base.Message = err.Error()
		return base
	}
	base.Verdict = Acceptable
	base.Algorithm = alg
	base.Digest = digest
	base.Message = "GitHub published a supported authoritative digest for this exact asset"
	return base
}

type Artifact struct {
	Path       string
	Temporary  bool
	Provenance Provenance
	Size       int64
}

func (a Artifact) Cleanup() error {
	if !a.Temporary || a.Path == "" {
		return nil
	}
	return os.RemoveAll(filepath.Dir(a.Path))
}

func (c *Client) Fetch(ctx context.Context, asset Asset, p Provenance) (Artifact, error) {
	if p.Verdict != Acceptable || p.Repository != asset.Repository || p.ReleaseID != asset.ReleaseID || p.AssetID != asset.ID || p.AssetState != "uploaded" || p.AssetSize != asset.Size || p.Algorithm == "" || p.Digest == "" || asset.Digest != p.Algorithm+":"+p.Digest {
		return Artifact{}, errors.New("artifact is not eligible for verified cache")
	}
	if c == nil || strings.TrimSpace(c.CacheRoot) == "" {
		return Artifact{}, errors.New("research artifact cache root is required")
	}
	key := cacheName(string(asset.Repository) + "\x00" + fmt.Sprint(asset.ReleaseID) + "\x00" + fmt.Sprint(asset.ID) + "\x00" + asset.State + "\x00" + p.Algorithm + "\x00" + p.Digest + "\x00" + fmt.Sprint(asset.Size))
	path := filepath.Join(c.CacheRoot, "artifacts", key)
	if st, e := os.Lstat(path); e == nil && (!st.Mode().IsRegular() || st.Mode()&os.ModeSymlink != 0) {
		_ = os.Remove(path)
	}
	cl := download.NewClient()
	if c != nil && c.HTTP != nil {
		cl.HTTP = c.HTTP
	}
	result, err := cl.FetchArtifact(ctx, download.ArtifactRequest{URL: asset.URL, Algorithm: p.Algorithm, Digest: p.Digest, Destination: path, MaxBytes: maxArtifactBytes})
	if err != nil {
		return Artifact{}, err
	}
	if result.Bytes != asset.Size {
		_ = os.Remove(path)
		return Artifact{}, fmt.Errorf("asset size mismatch: expected %d, got %d", asset.Size, result.Bytes)
	}
	return Artifact{Path: path, Provenance: p, Size: result.Bytes}, nil
}

func (c *Client) FetchUnverified(ctx context.Context, asset Asset) (Artifact, error) {
	dir, err := os.MkdirTemp(func() string {
		if c != nil {
			return c.TempRoot
		}
		return ""
	}(), "tarlink-research-")
	if err != nil {
		return Artifact{}, err
	}
	path := filepath.Join(dir, "artifact")
	cl := download.NewClient()
	if c != nil && c.HTTP != nil {
		cl.HTTP = c.HTTP
	}
	result, err := cl.FetchFile(ctx, download.FileRequest{URL: asset.URL, Destination: path, MaxBytes: maxArtifactBytes})
	if err != nil {
		_ = os.RemoveAll(dir)
		return Artifact{}, err
	}
	if result.Bytes != asset.Size {
		_ = os.RemoveAll(dir)
		return Artifact{}, fmt.Errorf("asset size mismatch: expected %d, got %d", asset.Size, result.Bytes)
	}
	return Artifact{Path: path, Temporary: true, Size: result.Bytes}, nil
}

type Inspection struct {
	ArtifactType    string            `json:"artifact_type"`
	ComputedDigests map[string]string `json:"computed_digests,omitempty"`
	Executables     []string          `json:"executables,omitempty"`
	Icons           []string          `json:"icons,omitempty"`
	Nested          []string          `json:"nested,omitempty"`
	Blockers        []string          `json:"blockers,omitempty"`
}

type InspectError struct {
	Kind  string
	Cause error
}

func (e *InspectError) Error() string { return e.Kind + ": " + e.Cause.Error() }
func (e *InspectError) Unwrap() error { return e.Cause }

const (
	InspectErrorCanceled    = "canceled"
	InspectErrorIO          = "io"
	InspectErrorUnsupported = "unsupported_artifact"
)

var inspectParserHook func() error

// Inspect analyzes one verified or locally sourced artifact. The expectedArch
// must come from TargetArchitecture, which derives it from artifact-name
// evidence only; "" means the asset name does not determine an architecture
// and is handled explicitly, never by falling back to the host architecture.
func Inspect(ctx context.Context, artifact Artifact, format archive.Format, expectedArch string) (Inspection, error) {
	if artifact.Path == "" {
		return Inspection{}, errors.New("artifact path is empty")
	}
	return inspectVerified(ctx, artifact, format, expectedArch)
}

// InspectAsset reacquires and verifies the exact immutable asset immediately
// before parsing. A cache mutation is treated as corruption and retried once.
// The expectedArch must come from TargetArchitecture, which derives it from
// artifact-name evidence only; "" means the asset name does not determine an
// architecture and is handled explicitly, never by falling back to the host
// architecture.
func (c *Client) InspectAsset(ctx context.Context, asset Asset, p Provenance, format archive.Format, expectedArch string) (Inspection, error) {
	a, err := c.Fetch(ctx, asset, p)
	if err != nil {
		return Inspection{}, err
	}
	r, err := inspectVerified(ctx, a, format, expectedArch)
	if err != nil && errors.Is(err, ErrCacheCorrupt) {
		_ = os.Remove(a.Path)
		a, err = c.Fetch(ctx, asset, p)
		if err != nil {
			return Inspection{}, err
		}
		r, err := inspectVerified(ctx, a, format, expectedArch)
		if errors.Is(err, ErrCacheCorrupt) {
			_ = os.Remove(a.Path)
		}
		return r, err
	}
	return r, err
}

func inspectVerified(ctx context.Context, artifact Artifact, format archive.Format, expectedArch string) (Inspection, error) {
	if ctx != nil && ctx.Err() != nil {
		return Inspection{}, &InspectError{Kind: InspectErrorCanceled, Cause: ctx.Err()}
	}
	entry, e := os.Lstat(artifact.Path)
	if e != nil || !entry.Mode().IsRegular() || entry.Mode()&os.ModeSymlink != 0 {
		return Inspection{}, ErrCacheCorrupt
	}
	f, e := os.Open(artifact.Path)
	if e != nil {
		return Inspection{}, fmt.Errorf("%w: open: %v", ErrCacheCorrupt, e)
	}
	defer f.Close()
	before, e := f.Stat()
	if e != nil || !before.Mode().IsRegular() || !os.SameFile(before, entry) {
		return Inspection{}, ErrCacheCorrupt
	}
	sha256Hash := sha256.New()
	sha512Hash := sha512.New()
	n, e := io.Copy(io.MultiWriter(sha256Hash, sha512Hash), io.LimitReader(f, maxArtifactBytes+1))
	if e != nil {
		return Inspection{}, fmt.Errorf("%w: read: %v", ErrCacheCorrupt, e)
	}
	if n > maxArtifactBytes || artifact.Size != 0 && n != artifact.Size {
		return Inspection{}, fmt.Errorf("%w: digest or size mismatch", ErrCacheCorrupt)
	}
	inspection := Inspection{ComputedDigests: map[string]string{
		"sha256": hex.EncodeToString(sha256Hash.Sum(nil)),
		"sha512": hex.EncodeToString(sha512Hash.Sum(nil)),
	}}
	if artifact.Provenance.Verdict == Acceptable {
		digest, ok := inspection.ComputedDigests[artifact.Provenance.Algorithm]
		if !ok || digest != artifact.Provenance.Digest || n != artifact.Size {
			return Inspection{}, fmt.Errorf("%w: digest or size mismatch", ErrCacheCorrupt)
		}
	}
	if inspectParserHook != nil {
		if e := inspectParserHook(); e != nil {
			return Inspection{}, e
		}
	}
	after, e := f.Stat()
	pathStat, pe := os.Lstat(artifact.Path)
	if e != nil || pe != nil || !after.Mode().IsRegular() || !pathStat.Mode().IsRegular() || pathStat.Mode()&os.ModeSymlink != 0 || !os.SameFile(after, pathStat) || after.Size() != before.Size() {
		return Inspection{}, ErrCacheCorrupt
	}
	if _, e := f.Seek(0, io.SeekStart); e != nil {
		return Inspection{}, fmt.Errorf("%w: seek: %v", ErrCacheCorrupt, e)
	}
	if format == "appimage" {
		copyDir, ce := os.MkdirTemp("", "tarlink-appimage-")
		if ce != nil {
			return Inspection{}, ce
		}
		defer os.RemoveAll(copyDir)
		copyPath := filepath.Join(copyDir, "artifact")
		out, ce := os.OpenFile(copyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if ce != nil {
			return Inspection{}, ce
		}
		var dst io.Writer = out
		var hashCopy hash.Hash
		if artifact.Provenance.Verdict == Acceptable {
			var he error
			hashCopy, he = checksum.NewHasher(artifact.Provenance.Algorithm, artifact.Provenance.Digest)
			if he != nil {
				_ = out.Close()
				return Inspection{}, fmt.Errorf("%w: copy hasher: %v", ErrCacheCorrupt, he)
			}
			dst = io.MultiWriter(out, hashCopy)
		}
		var copied int64
		copied, ce = io.Copy(dst, f)
		if ce == nil && artifact.Provenance.Verdict == Acceptable && (copied != artifact.Size || hex.EncodeToString(hashCopy.Sum(nil)) != artifact.Provenance.Digest) {
			ce = ErrCacheCorrupt
		}
		if ce == nil {
			ce = out.Close()
		} else {
			_ = out.Close()
		}
		if ce != nil {
			return Inspection{}, ce
		}
		var err error
		if expectedArch != "" {
			err = appimage.ValidatePath(copyPath, expectedArch)
		} else {
			err = appimage.ValidateSupportedTarget(copyPath)
		}
		if err != nil {
			if strings.Contains(err.Error(), "architecture mismatch") {
				return Inspection{ArtifactType: "appimage", ComputedDigests: inspection.ComputedDigests, Blockers: []string{"UNSUPPORTED_ARCH"}}, nil
			}
			return Inspection{ArtifactType: "appimage", ComputedDigests: inspection.ComputedDigests, Blockers: []string{"UNSUPPORTED_ARTIFACT"}}, nil
		}
		return Inspection{ArtifactType: "appimage", ComputedDigests: inspection.ComputedDigests}, nil
	}
	if format == "" {
		b := make([]byte, 6)
		n, e := io.ReadFull(f, b)
		if e != nil && e != io.EOF && e != io.ErrUnexpectedEOF {
			return Inspection{}, e
		}
		b = b[:n]
		if bytes.HasPrefix(b, []byte("PK")) {
			format = archive.FormatZip
		} else if bytes.HasPrefix(b, []byte{0x1f, 0x8b}) {
			format = archive.FormatTarGz
		} else if bytes.HasPrefix(b, []byte{0xfd, '7', 'z', 'X', 'Z', 0}) {
			format = archive.FormatTarXZ
		} else {
			return Inspection{ArtifactType: "unknown", ComputedDigests: inspection.ComputedDigests, Blockers: []string{"UNSUPPORTED_ARTIFACT"}}, nil
		}
	}
	root, err := os.MkdirTemp(filepath.Dir(artifact.Path), ".tarlink-inspect-")
	if err != nil {
		return Inspection{}, err
	}
	defer os.RemoveAll(root)
	if _, e := f.Seek(0, io.SeekStart); e != nil {
		return Inspection{}, e
	}
	if err := archive.Extract(ctx, f, root, format, archive.DefaultLimits()); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Inspection{}, &InspectError{Kind: InspectErrorCanceled, Cause: err}
		}
		return Inspection{ArtifactType: string(format), ComputedDigests: inspection.ComputedDigests, Blockers: []string{"UNSUPPORTED_ARTIFACT"}}, nil
	}
	inspection.ArtifactType = string(format)
	result := inspection
	type candidate struct {
		path  string
		score int
	}
	var executableCandidates []candidate
	var iconCandidates []candidate
	walkErr := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info == nil {
			return nil
		}
		if info.Mode().IsRegular() {
			rel := strings.TrimPrefix(path, root+string(os.PathSeparator))
			f, e := os.Open(path)
			if e != nil {
				return e
			}
			// A single bounded read supplies all static evidence used for this
			// pass. Never execute or invoke a type detector on extracted files.
			var evidence [4096]byte
			n, e := io.ReadFull(f, evidence[:])
			_ = f.Close()
			if e != nil && e != io.ErrUnexpectedEOF && e != io.EOF {
				return e
			}
			if score := executableCandidateScore(rel, info, evidence[:n]); score >= 80 {
				executableCandidates = append(executableCandidates, candidate{path: rel, score: score})
			}
			if score := archiveIconCandidateScore(rel, info, evidence[:n]); score > 0 {
				iconCandidates = append(iconCandidates, candidate{path: rel, score: score})
			}
			if (strings.HasSuffix(strings.ToLower(info.Name()), ".zip") && n >= 2 && bytes.Equal(evidence[:2], []byte("PK"))) || (strings.HasSuffix(strings.ToLower(info.Name()), ".tar.gz") && n >= 2 && bytes.Equal(evidence[:2], []byte{0x1f, 0x8b})) || (strings.HasSuffix(strings.ToLower(info.Name()), ".tar.xz") && n >= 6 && bytes.Equal(evidence[:6], []byte{0xfd, '7', 'z', 'X', 'Z', 0})) {
				result.Nested = append(result.Nested, rel)
			}
		}
		return nil
	})
	if walkErr != nil {
		return Inspection{}, walkErr
	}
	sort.Slice(executableCandidates, func(i, j int) bool {
		if executableCandidates[i].score != executableCandidates[j].score {
			return executableCandidates[i].score > executableCandidates[j].score
		}
		return executableCandidates[i].path < executableCandidates[j].path
	})
	if len(executableCandidates) != 0 {
		// Retain only candidates close to the strongest static evidence. This
		// avoids treating bundled build fixtures and helper tools as peers of a
		// root application executable, while preserving genuine ties for review.
		minimum := executableCandidates[0].score - 10
		for _, item := range executableCandidates {
			if item.score < minimum {
				break
			}
			result.Executables = append(result.Executables, item.path)
		}
	}
	sort.Slice(iconCandidates, func(i, j int) bool {
		if iconCandidates[i].score != iconCandidates[j].score {
			return iconCandidates[i].score > iconCandidates[j].score
		}
		return iconCandidates[i].path < iconCandidates[j].path
	})
	if len(iconCandidates) != 0 {
		minimum := iconCandidates[0].score - 10
		for _, item := range iconCandidates {
			if item.score < minimum {
				break
			}
			result.Icons = append(result.Icons, item.path)
		}
	}
	if len(result.Executables) == 0 {
		result.Blockers = append(result.Blockers, "NO_EXECUTABLE")
	}
	return result, nil
}

func executableCandidateScore(rel string, info os.FileInfo, evidence []byte) int {
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return 0
	}
	elf := len(evidence) >= 4 && bytes.Equal(evidence[:4], []byte{0x7f, 'E', 'L', 'F'})
	shebang := bytes.HasPrefix(evidence, []byte("#!"))
	executableMode := info.Mode()&0o111 != 0
	if !elf && !shebang && !executableMode {
		return 0
	}
	score := 40
	if executableMode {
		score += 30
	}
	if elf {
		score += 30
	} else if shebang {
		score += 20
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) == 1 {
		score += 30
	}
	for _, part := range parts[:len(parts)-1] {
		switch strings.ToLower(part) {
		case "bin", "sbin":
			score += 20
		case "test", "tests", "doc", "docs", "examples", "example", "tools", "tool", "scripts", "lib", "vendor", ".github":
			score -= 50
		}
	}
	return score
}

func archiveIconCandidateScore(rel string, info os.FileInfo, evidence []byte) int {
	if info == nil || !info.Mode().IsRegular() || info.Size() < 0 || info.Size() > maxArchiveIconBytes {
		return 0
	}
	ext := strings.ToLower(filepath.Ext(rel))
	if ext != ".png" && ext != ".svg" {
		return 0
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	base := strings.ToLower(parts[len(parts)-1])
	base = strings.TrimSuffix(base, ext)
	score := 0
	if base == "icon" || base == "logo" || strings.Contains(base, "icon") || strings.Contains(base, "logo") {
		score += 60
	}
	for _, part := range parts[:len(parts)-1] {
		if strings.EqualFold(part, "icon") || strings.EqualFold(part, "icons") {
			score += 40
			break
		}
		switch strings.ToLower(part) {
		case "doc", "docs", "test", "tests", "examples", "example", "lib", "vendor", ".github":
			score -= 50
		}
	}
	if score < 60 {
		return 0
	}
	// A PNG signature gives a small additional signal; SVG is deliberately
	// accepted by extension because valid SVG files begin with optional XML,
	// comments, or whitespace rather than one fixed magic sequence.
	if ext == ".png" && len(evidence) >= 8 && bytes.Equal(evidence[:8], []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}) {
		score += 10
	}
	return score
}
