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
	"regexp"
	"strings"
	"time"
)

const (
	maxResponseBytes = 4 << 20
	githubAPIHost    = "api.github.com"
)

var repositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// Target identifies an explicitly reviewed upstream source and the releases
// currently admitted to the registry. The caller supplies this metadata from
// the validated registry; discovery does not infer it from upstream.
type Target struct {
	App        string
	Repository string
	Channel    string
	Approved   []ApprovedRelease
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
	HTTP *http.Client
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
			approved[release.Version] = true
		}
	}
	apiURL, err := APIURL(target.Repository)
	if err != nil {
		return nil, err
	}
	apiURL += "?per_page=100"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "tarlink-registry-freshness")
	var httpClient *http.Client
	if c != nil {
		httpClient = c.HTTP
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	response, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("discover GitHub releases: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub releases API returned %s", response.Status)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read GitHub releases: %w", err)
	}
	if int64(len(data)) > maxResponseBytes {
		return nil, errors.New("GitHub releases response exceeds size limit")
	}
	var releases []githubRelease
	if err := json.Unmarshal(data, &releases); err != nil {
		return nil, fmt.Errorf("decode GitHub releases: %w", err)
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
		version := canonicalTagVersion(release.TagName)
		if approved[release.TagName] || approved[version] {
			continue
		}
		result = append(result, Candidate{App: target.App, Repository: target.Repository, Channel: target.Channel, Version: version, UpstreamURL: release.HTMLURL, PublishedAt: release.PublishedAt, Prerelease: release.Prerelease})
	}
	return result, nil
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
