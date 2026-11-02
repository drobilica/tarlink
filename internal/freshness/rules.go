package freshness

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
)

// Decision explains which compiled rule applies. Manifest URLs are diagnostic
// evidence only and never authorize a new network source.
type Decision struct {
	Repository string `json:"repository,omitempty"`
	Rule       string `json:"rule,omitempty"`
	Reason     string `json:"reason,omitempty"`
	Detail     string `json:"detail,omitempty"`
}

func Resolve(appID string, artifactURLs []string) (Decision, error) {
	repository, ok, err := Repository(appID)
	if err != nil {
		return Decision{}, err
	}
	if ok {
		return Decision{Repository: repository, Rule: "reviewed_github_releases"}, nil
	}
	sources := map[string]bool{}
	for _, raw := range artifactURLs {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Port() != "" {
			return Decision{Reason: "invalid_upstream", Detail: "Artifact source is not a canonical HTTPS URL; review its registry metadata."}, nil
		}
		source := u.Hostname()
		if source == "github.com" {
			parts := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(parts) >= 2 && repositoryPattern.MatchString(parts[0]+"/"+parts[1]) {
				source += "/" + parts[0] + "/" + parts[1]
			}
		}
		sources[source] = true
	}
	names := make([]string, 0, len(sources))
	for source := range sources {
		names = append(names, source)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return Decision{Reason: "missing_upstream", Detail: "No artifact source is available to identify an update provider."}, nil
	}
	if len(names) > 1 {
		return Decision{Reason: "ambiguous_upstream", Detail: "Current artifacts name different sources: " + strings.Join(names, ", ") + ". A reviewed source rule is required."}, nil
	}
	source := names[0]
	if strings.HasPrefix(source, "github.com/") {
		return Decision{Reason: "unreviewed_repository", Detail: source + " has no reviewed TarLink discovery rule; add one after verifying the release source."}, nil
	}
	if source == "gitlab.com" {
		return Decision{Reason: "unsupported_provider", Detail: "GitLab release discovery is not implemented; check the project's releases manually."}, nil
	}
	return Decision{Reason: "unsupported_provider", Detail: fmt.Sprintf("No release discovery adapter for %s; a reviewed provider adapter or GitHub source rule is needed.", source)}, nil
}

// MatchesAsset narrows shared-repository applications to their reviewed asset
// prefix before the existing platform, format, digest and inspection checks.
func MatchesAsset(appID, asset string) bool {
	rule, ok := approvedRules[appID]
	return !ok || ((rule.AssetPrefix == "" || strings.HasPrefix(asset, rule.AssetPrefix)) && (rule.AssetExclude == "" || !strings.Contains(asset, rule.AssetExclude)))
}

// Issue gives reports a stable reason code and an actionable explanation.
type Issue struct{ Reason, Detail string }

func (e *Issue) Error() string { return e.Detail }
