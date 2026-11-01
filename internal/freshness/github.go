// Package freshness discovers upstream release candidates for registry
// maintainers. Its results are advisory only: this package never produces
// manifest data, digests, approvals, or installation requests.
package freshness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	maxResponseBytes = 4 << 20
	githubAPIHost    = "api.github.com"
)

var numericVersionPattern = regexp.MustCompile(`^[0-9]+(\.[0-9]+)*$`)

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// Target identifies an explicitly reviewed upstream source and the releases
// currently admitted to the registry. The caller supplies this metadata from
// the validated registry; discovery does not infer it from upstream.
type Target struct {
	App        string
	Repository string
	Channel    string
	Approved   []ApprovedRelease
	Current    string
}

// ApprovedRelease is intentionally only a version/channel pair. Artifact
// URLs and checksums remain registry trust data and are never obtained here.
type ApprovedRelease struct {
	Version string
	Channel string
}

type Candidate struct {
	App         string    `json:"app"`
	Repository  string    `json:"repository"`
	Channel     string    `json:"channel"`
	Version     string    `json:"version"`
	UpstreamURL string    `json:"upstream_url"`
	PublishedAt time.Time `json:"published_at,omitempty"`
	Prerelease  bool      `json:"prerelease"`
}

// Report is the complete read-only result for one invocation.
type Report struct {
	Candidates []Candidate `json:"candidates"`
}

// Client accesses only GitHub's public Releases API. HTTP may be replaced in
// tests; production callers should use http.DefaultClient or a bounded client.
type Client struct {
	HTTP  *http.Client
	mu    sync.Mutex
	cache map[string]releaseLookup
}

type releaseLookup struct {
	releases []githubRelease
	err      error
}

// ForScan creates disposable memoization scoped to one explicit scan.
func (c *Client) ForScan() *Client {
	var client *http.Client
	if c != nil {
		client = c.HTTP
	}
	return &Client{HTTP: client, cache: make(map[string]releaseLookup)}
}

type githubRelease struct {
	TagName     string    `json:"tag_name"`
	HTMLURL     string    `json:"html_url"`
	Draft       bool      `json:"draft"`
	Prerelease  bool      `json:"prerelease"`
	PublishedAt time.Time `json:"published_at"`
}

func (c *Client) Discover(ctx context.Context, target Target) ([]Candidate, error) {
	if strings.TrimSpace(target.App) == "" {
		return nil, errors.New("freshness target app is required")
	}
	if !repositoryPattern.MatchString(target.Repository) {
		return nil, fmt.Errorf("invalid GitHub repository %q", target.Repository)
	}
	if strings.TrimSpace(target.Channel) == "" {
		return nil, errors.New("freshness target channel is required")
	}
	approved := make(map[string]bool, len(target.Approved))
	for _, release := range target.Approved {
		if release.Version != "" && release.Channel == target.Channel {
			approved[ReleaseVersion(target.Repository, release.Version)] = true
		}
	}
	releases, err := c.lookup(ctx, target.Repository)
	if err != nil {
		return nil, err
	}
	var currentPublished time.Time
	for _, release := range releases {
		if !release.Draft && (target.Channel == "stable") != release.Prerelease && ReleaseVersion(target.Repository, release.TagName) == ReleaseVersion(target.Repository, target.Current) {
			currentPublished = release.PublishedAt
			break
		}
	}
	result := make([]Candidate, 0)
	for _, release := range releases {
		// Drafts have not been published and are never useful candidates.
		if release.Draft || strings.TrimSpace(release.TagName) == "" {
			continue
		}
		// A stable channel must not consume prereleases. Conversely, a
		// prerelease channel (nightly, beta, preview, development, or an
		// explicitly named equivalent) must not report stable releases. This
		// keeps one upstream release from being presented as every channel.
		if target.Channel == "stable" && release.Prerelease {
			continue
		}
		if target.Channel != "stable" && !release.Prerelease {
			continue
		}
		version := ReleaseVersion(target.Repository, release.TagName)
		if approved[release.TagName] || approved[version] {
			continue
		}
		if target.Current != "" {
			newer, err := IsNewerRelease(version, target.Current, release.PublishedAt, currentPublished)
			if err != nil {
				return nil, &Issue{Reason: "unknown_release_order", Detail: err.Error()}
			}
			if !newer {
				continue
			}
		}
		result = append(result, Candidate{App: target.App, Repository: target.Repository, Channel: target.Channel, Version: version, UpstreamURL: release.HTMLURL, PublishedAt: release.PublishedAt, Prerelease: release.Prerelease})
	}
	return result, nil
}

func (c *Client) lookup(ctx context.Context, repository string) ([]githubRelease, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c == nil || c.cache == nil {
		return c.fetch(ctx, repository)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if cached, ok := c.cache[repository]; ok {
		return cached.releases, cached.err
	}
	releases, err := c.fetch(ctx, repository)
	if ctx.Err() == nil {
		c.cache[repository] = releaseLookup{releases: releases, err: err}
	}
	return releases, err
}

func (c *Client) fetch(ctx context.Context, repository string) ([]githubRelease, error) {
	apiURL, err := APIURL(repository)
	if err != nil {
		return nil, err
	}
	var releases []githubRelease
	for page := 1; page <= 2; page++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s?per_page=50&page=%d", apiURL, page), nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("User-Agent", "tarlink-registry-freshness")
		if token := strings.TrimSpace(os.Getenv("GH_TOKEN")); token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		var httpClient *http.Client
		if c != nil {
			httpClient = c.HTTP
		}
		if httpClient == nil {
			httpClient = http.DefaultClient
		}
		bounded := *httpClient
		if bounded.Timeout <= 0 || bounded.Timeout > 30*time.Second {
			bounded.Timeout = 30 * time.Second
		}
		bounded.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		response, err := bounded.Do(req)
		if err != nil {
			return nil, fmt.Errorf("discover GitHub releases: %w", err)
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			return nil, responseIssue(response)
		}
		data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
		response.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("read GitHub releases: %w", err)
		}
		if int64(len(data)) > maxResponseBytes {
			return nil, &Issue{Reason: "response_too_large", Detail: "GitHub releases response exceeds the 4 MiB limit; a narrower provider query needs review."}
		}
		var batch []githubRelease
		if err := json.Unmarshal(data, &batch); err != nil {
			return nil, &Issue{Reason: "invalid_metadata", Detail: fmt.Sprintf("Cannot decode GitHub release metadata: %v", err)}
		}
		if len(batch) > 50 {
			return nil, errors.New("GitHub releases page exceeds release limit")
		}
		releases = append(releases, batch...)
		if len(batch) < 50 {
			break
		}
	}
	return releases, nil
}

// canonicalTagVersion handles the conventional GitHub spelling for numeric
// release versions. Other identifiers remain opaque: in particular, vNightly
// must not silently become Nightly and collide with a separately reviewed ID.
func canonicalTagVersion(tag string) string {
	if len(tag) > 1 && tag[0] == 'v' && tag[1] >= '0' && tag[1] <= '9' {
		return tag[1:]
	}
	return tag
}

// APIURL validates the only URL shape used by this package. It is exported
// for callers that want to document or test the provider boundary.
func APIURL(repository string) (string, error) {
	if !repositoryPattern.MatchString(repository) {
		return "", fmt.Errorf("invalid GitHub repository %q", repository)
	}
	u, _ := url.Parse("https://" + githubAPIHost + "/repos/" + repository + "/releases")
	return u.String(), nil
}

// ReleaseVersion maps reviewed upstream tag conventions to registry versions.
// Reviewed prefixes and suffixes are stripped only around numeric versions.
// Other repositories and opaque tags retain their existing spelling.
func ReleaseVersion(repository, tag string) string {
	version := canonicalTagVersion(tag)
	for _, rule := range approvedRules {
		if rule.Repository != repository {
			continue
		}
		normalized := strings.TrimPrefix(strings.TrimSuffix(version, rule.VersionSuffix), rule.VersionPrefix)
		if numericVersionPattern.MatchString(normalized) {
			return normalized
		}
		break
	}
	return version
}

func responseIssue(response *http.Response) error {
	reason := "lookup_failed"
	detail := fmt.Sprintf("GitHub releases API returned %s", response.Status)
	switch response.StatusCode {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		reason = "repository_moved"
		detail = "GitHub redirected the reviewed repository; verify its new owner/name and update the built-in rule. Redirects are not followed."
	case http.StatusNotFound:
		reason = "repository_not_found"
		detail = "The reviewed GitHub repository was not found or is no longer public; verify its source rule."
	case http.StatusTooManyRequests:
		reason = "rate_limited"
		detail = "GitHub API quota is exhausted; wait for reset or use GH_TOKEN for authenticated metadata discovery."
	case http.StatusForbidden:
		if response.Header.Get("X-RateLimit-Remaining") == "0" || response.Header.Get("Retry-After") != "" {
			reason = "rate_limited"
			detail = "GitHub API quota is exhausted; wait for reset or use GH_TOKEN for authenticated metadata discovery."
		}
	case http.StatusUnauthorized:
		reason = "authentication_failed"
		detail = "GitHub rejected the metadata credential; check GH_TOKEN."
	}
	return &Issue{Reason: reason, Detail: detail}
}
