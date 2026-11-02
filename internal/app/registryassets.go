package app

import (
	"fmt"
	"sort"
	"strings"

	"github.com/drobilica/tarlink/internal/freshness"
)

func assessRegistryAssets(appID, platform, format string, names []string) freshness.AssetAssessment {
	result := freshness.AssetAssessment{Platform: platform, Format: format, Matches: []string{}, Rejected: []freshness.RejectedAsset{}}
	names = append([]string(nil), names...)
	sort.Strings(names)
	for _, name := range names {
		reason := ""
		switch {
		case !freshness.MatchesAsset(appID, name):
			reason = "excluded by reviewed application rule"
		case registryAssetFormat(name) != format:
			reason = fmt.Sprintf("format %q does not match %q", registryAssetFormat(name), format)
		case registryUpdateAssetPlatform(name) == "":
			reason = "no unambiguous Linux architecture in filename"
		case registryUpdateAssetPlatform(name) != platform:
			reason = "platform " + registryUpdateAssetPlatform(name) + " does not match " + platform
		}
		if reason == "" {
			result.Matches = append(result.Matches, name)
		} else {
			result.Rejected = append(result.Rejected, freshness.RejectedAsset{Name: name, Reason: reason})
		}
	}
	result.Status = "matched"
	if len(result.Matches) == 0 {
		result.Status = "missing"
	} else if len(result.Matches) > 1 {
		result.Status = "ambiguous"
	}
	return result
}

func describeRegistryAssets(result freshness.AssetAssessment) string {
	details := []string{fmt.Sprintf("%s: %d matching %s assets for %s (filename assessment; bytes unverified)", result.Status, len(result.Matches), result.Format, result.Platform)}
	for _, name := range result.Matches {
		details = append(details, fmt.Sprintf("%q: matches", name))
	}
	for _, asset := range result.Rejected {
		details = append(details, fmt.Sprintf("%q: %s", asset.Name, asset.Reason))
	}
	if len(result.Matches)+len(result.Rejected) == 0 {
		details = append(details, "release has no assets")
	}
	return strings.Join(details, "; ")
}
