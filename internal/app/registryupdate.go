package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"time"

	"github.com/drobilica/tarlink/internal/download"
	"github.com/drobilica/tarlink/internal/freshness"
	"github.com/drobilica/tarlink/internal/manifest"
	"github.com/drobilica/tarlink/internal/registry"
)

// RegistryUpdateService checks the complete official catalog for advisory
// upstream candidates. It never edits manifests or changes application state.
type RegistryUpdateService interface {
	UpdateRegistry(context.Context, RegistryUpdateOptions, ProgressSink) (RegistryUpdateReport, error)
}

// RegistryUpdateOptions controls the advisory report, never registry contents.
type RegistryUpdateOptions struct {
	// Limit caps missing releases per app/platform across channels; zero means all.
	Limit int
}

type RegistryUpdateReport struct {
	SchemaVersion int                    `json:"schema_version"`
	Revision      string                 `json:"revision"`
	CheckedAt     time.Time              `json:"checked_at"`
	Results       []RegistryUpdateResult `json:"results"`
	Summary       RegistryUpdateSummary  `json:"summary"`
}

type RegistryUpdateSummary struct {
	Total        int            `json:"total"`
	Candidates   int            `json:"candidates"`
	NoCandidates int            `json:"no_candidates"`
	Skipped      int            `json:"skipped"`
	Errors       int            `json:"errors"`
	SkipReasons  map[string]int `json:"skip_reasons"`
}

type RegistryUpdateResult struct {
	Repository string                `json:"repository,omitempty"`
	Rule       string                `json:"rule,omitempty"`
	Detail     string                `json:"detail,omitempty"`
	App        string                `json:"app"`
	Platform   string                `json:"platform"`
	Status     string                `json:"status"`
	Reason     string                `json:"reason,omitempty"`
	Error      string                `json:"error,omitempty"`
	Candidates []freshness.Candidate `json:"candidates"`
}

func (core *Core) UpdateRegistry(ctx context.Context, options RegistryUpdateOptions, sink ProgressSink) (RegistryUpdateReport, error) {
	report := RegistryUpdateReport{SchemaVersion: 1, Results: []RegistryUpdateResult{}}
	if options.Limit < 0 {
		return report, errors.New("registry update limit must not be negative")
	}
	if core.offline() {
		return report, download.ErrOffline
	}
	// An explicit registry update must compare with a freshly fetched snapshot;
	// a failed refresh cannot silently turn a stale catalog into a successful scan.
	if _, err := core.SyncRegistry(ctx, sink); err != nil {
		return report, err
	}
	catalog, err := registry.Open(filepath.Join(core.layout.Cache, "registry"))
	if err != nil {
		return report, classify("registry update", err)
	}
	client := download.NewClient().HTTP
	if core.syncer.Client != nil && core.syncer.Client.HTTP != nil {
		copy := *core.syncer.Client.HTTP
		client = &copy
	}
	client.Timeout = 30 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return checkRegistryUpdates(ctx, catalog, &freshness.Client{HTTP: client}, options)
}

func checkRegistryUpdates(ctx context.Context, catalog *registry.Catalog, client *freshness.Client, options RegistryUpdateOptions) (report RegistryUpdateReport, scanErr error) {
	report = RegistryUpdateReport{SchemaVersion: 1, Revision: catalog.Revision, CheckedAt: catalog.FetchedAt, Results: []RegistryUpdateResult{}}
	defer func() {
		report.Summary = RegistryUpdateSummary{Total: len(report.Results), SkipReasons: map[string]int{}}
		for _, r := range report.Results {
			switch r.Status {
			case "candidates":
				report.Summary.Candidates++
			case "no-candidates":
				report.Summary.NoCandidates++
			case "skipped":
				report.Summary.Skipped++
				report.Summary.SkipReasons[r.Reason]++
			case "error":
				report.Summary.Errors++
			}
		}
	}()
	client = client.ForScan()
	ids := make([]string, 0, len(catalog.Variants))
	for id := range catalog.Variants {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var failures []error
	for _, id := range ids {
		platforms := make([]manifest.Platform, 0, len(catalog.Variants[id]))
		for platform := range catalog.Variants[id] {
			platforms = append(platforms, platform)
		}
		sort.Slice(platforms, func(i, j int) bool {
			return platforms[i].OS+"-"+platforms[i].Arch < platforms[j].OS+"-"+platforms[j].Arch
		})
		for _, platform := range platforms {
			if err := ctx.Err(); err != nil {
				return report, errors.Join(append(failures, err)...)
			}
			item := catalog.Variants[id][platform]
			result := RegistryUpdateResult{App: id, Platform: platform.OS + "-" + platform.Arch, Candidates: []freshness.Candidate{}}
			decision, err := freshness.Resolve(id, releaseSourceURLs(item))
			result.Repository, result.Rule, result.Detail = decision.Repository, decision.Rule, decision.Detail
			switch {
			case err != nil:
				return report, err
			case !item.WantsEnabled():
				result.Status, result.Reason = "skipped", "disabled"
				result.Detail = "This application is disabled in the registry."
			case decision.Repository == "":
				result.Status, result.Reason = "skipped", decision.Reason
			default:
				result.Status = "no-candidates"
				approved := make([]freshness.ApprovedRelease, 0, len(item.ReleaseHistory.Releases))
				for _, release := range item.ReleaseHistory.Releases {
					approved = append(approved, freshness.ApprovedRelease{Version: release.Version, Channel: release.Channel})
				}
				channels := make([]string, 0, len(item.ReleaseHistory.Channels))
				for channel := range item.ReleaseHistory.Channels {
					channels = append(channels, channel)
				}
				sort.Strings(channels)
				for _, channel := range channels {
					candidates, err := client.Discover(ctx, freshness.Target{App: id, Repository: decision.Repository, Channel: channel, Approved: approved, Current: item.ReleaseHistory.Channels[channel].Current})
					if err != nil {
						result.Status, result.Reason = "error", "lookup_failed"
						result.Detail = err.Error()
						var issue *freshness.Issue
						if errors.As(err, &issue) {
							result.Reason, result.Detail = issue.Reason, issue.Detail
						}
						result.Error = fmt.Sprintf("%s: %v", channel, err)
						failures = append(failures, fmt.Errorf("%s/%s/%s: %w", id, result.Platform, channel, err))
						break
					}
					result.Candidates = append(result.Candidates, candidates...)
				}
				// Stable sorting preserves discovery order when publication times tie
				// or are absent. Version strings need not be semantic versions.
				sort.SliceStable(result.Candidates, func(i, j int) bool {
					return result.Candidates[i].PublishedAt.After(result.Candidates[j].PublishedAt)
				})
				if options.Limit > 0 && len(result.Candidates) > options.Limit {
					result.Candidates = result.Candidates[:options.Limit]
				}
				if result.Status != "error" && len(result.Candidates) > 0 {
					result.Status = "candidates"
				}
			}
			report.Results = append(report.Results, result)
		}
	}
	return report, errors.Join(failures...)
}

// Only current channel heads are diagnostic source evidence. Old retained URLs
// may belong to previous upstream owners and must not create false ambiguity.
func releaseSourceURLs(item *manifest.Manifest) []string {
	var urls []string
	for channel, head := range item.ReleaseHistory.Channels {
		for _, release := range item.ReleaseHistory.Releases {
			if release.Channel == channel && release.Version == head.Current {
				urls = append(urls, release.URL)
			}
		}
	}
	sort.Strings(urls)
	return urls
}
