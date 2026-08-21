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
	repository, ok := freshnessRepositories[appID]
	if !ok {
		return freshness.Report{}, &Error{Code: CodeInvalidArguments, Op: "registry freshness", Err: fmt.Errorf("no explicitly approved GitHub repository mapping for %q", appID)}
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
	client := &freshness.Client{}
	for _, channel := range channels {
		candidates, discoverErr := client.Discover(ctx, freshness.Target{App: appID, Repository: repository, Channel: channel, Approved: approved})
		if discoverErr != nil {
			return freshness.Report{}, classify("registry freshness "+appID+"@"+channel, discoverErr)
		}
		report.Candidates = append(report.Candidates, candidates...)
	}
	return report, nil
}

// Keep this mapping deliberately explicit. It is maintainer configuration,
// not metadata discovered from GitHub or from an untrusted registry field.
var freshnessRepositories = map[string]string{
	"pcsx2":             "PCSX2/pcsx2",
	"xemu":              "xemu-project/xemu",
	"melonds":           "melonDS-emu/melonDS",
	"ppsspp":            "hrydgard/ppsspp",
	"openrct2":          "OpenRCT2/OpenRCT2",
	"openttd":           "OpenTTD/OpenTTD",
	"steam-rom-manager": "SteamGridDB/steam-rom-manager",
}
