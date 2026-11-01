package app

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/drobilica/tarlink/internal/filesystem"
	"github.com/drobilica/tarlink/internal/freshness"
	"github.com/drobilica/tarlink/internal/registry"
)

// Freshness reports advisory upstream releases for one application. It only
// reads the already validated registry cache; candidates cannot alter the
// catalog, state, or installation lifecycle.
func (core *Core) Freshness(ctx context.Context, appID string) (freshness.Report, error) {
	if err := filesystem.ValidateID(appID); err != nil {
		return freshness.Report{}, &Error{Code: CodeInvalidArguments, Op: "registry freshness", Err: err}
	}
	catalog, err := registry.Open(filepath.Join(core.layout.Cache, "registry"))
	if err != nil {
		return freshness.Report{}, classify("registry freshness", err)
	}
	goos, goarch := core.platform()
	item, err := catalog.ManifestForPlatform(appID, goos, goarch)
	if err != nil {
		return freshness.Report{}, classify("registry freshness", err)
	}
	if !item.WantsEnabled() {
		return freshness.Report{}, &Error{
			Code: CodeInvalidArguments,
			Op:   "registry freshness",
			Err:  fmt.Errorf("application %q is disabled", appID),
		}
	}
	decision, err := freshness.Resolve(appID, releaseSourceURLs(item))
	if err != nil {
		return freshness.Report{}, classify("registry freshness", err)
	}
	if decision.Repository == "" {
		return freshness.Report{}, &Error{Code: CodeInvalidArguments, Op: "registry freshness", Err: fmt.Errorf("%s: %s", decision.Reason, decision.Detail)}
	}
	channels := make([]string, 0, len(item.ReleaseHistory.Channels))
	for channel := range item.ReleaseHistory.Channels {
		channels = append(channels, channel)
	}
	sort.Strings(channels)
	approved := make([]freshness.ApprovedRelease, 0, len(item.ReleaseHistory.Releases))
	for _, release := range item.ReleaseHistory.Releases {
		approved = append(approved, freshness.ApprovedRelease{Version: release.Version, Channel: release.Channel})
	}
	report := freshness.Report{Candidates: make([]freshness.Candidate, 0)}
	client := (&freshness.Client{}).ForScan()
	for _, channel := range channels {
		candidates, discoverErr := client.Discover(ctx, freshness.Target{App: appID, Repository: decision.Repository, Channel: channel, Approved: approved})
		if discoverErr != nil {
			return freshness.Report{}, classify("registry freshness "+appID+"@"+channel, discoverErr)
		}
		report.Candidates = append(report.Candidates, candidates...)
	}
	return report, nil
}
