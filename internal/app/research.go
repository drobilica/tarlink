package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/drobilica/tarlink/internal/archive"
	"github.com/drobilica/tarlink/internal/research"
)

type ResearchOptions struct {
	Repository string
	Release    string
	Asset      string
	Refresh    bool
	Inspect    bool
}

type ResearchResult struct {
	Repository research.Repository  `json:"repository"`
	Release    research.Release     `json:"release"`
	Asset      research.Asset       `json:"asset"`
	Provenance research.Provenance  `json:"provenance"`
	Inspection *research.Inspection `json:"inspection,omitempty"`
	Status     string               `json:"status,omitempty"`
	Error      *ResearchError       `json:"error,omitempty"`
}

type ResearchError struct {
	Kind       string `json:"kind,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
	ReasonCode string `json:"reason_code"`
	Message    string `json:"message"`
}

type ResearchFailure struct {
	ReasonCode string
	// Kind and HTTPStatus preserve the upstream failure class for human and
	// machine callers. They deliberately contain no response headers or
	// credentials.
	Kind       string
	HTTPStatus int
	Result     ResearchResult
	Err        error
}

func (e *ResearchFailure) Error() string { return e.Err.Error() }
func (e *ResearchFailure) Unwrap() error { return e.Err }

func (m *Maintainer) Research(ctx context.Context, options ResearchOptions) (ResearchResult, error) {
	repo, err := research.ParseRepository(options.Repository)
	if err != nil {
		return ResearchResult{}, &ResearchFailure{ReasonCode: "INVALID_REPOSITORY", Err: err}
	}
	client := &research.Client{CacheRoot: filepath.Join(m.layout.Cache, "registry-research"), Refresh: options.Refresh}
	if m.client != nil {
		client.HTTP = m.client.HTTP
	}
	var releases []research.Release
	// A tag selector has an exact GitHub endpoint in the research client when
	// available. Keep the interface assertion here so the application facade
	// remains compatible with the shared discovery implementation while older
	// clients can still use the bounded list endpoint.
	if options.Release != "" {
		if exact, ok := any(client).(interface {
			DiscoverRelease(context.Context, string, string) (research.Release, error)
		}); ok {
			selected, discoverErr := exact.DiscoverRelease(ctx, string(repo), options.Release)
			if discoverErr != nil {
				failure := researchFailure(discoverErr)
				if failure.ReasonCode == "REPOSITORY_NOT_FOUND" {
					failure.ReasonCode = "RELEASE_NOT_FOUND"
				}
				failure.Result = errorResearchResult(repo, failure)
				return failure.Result, failure
			}
			releases = []research.Release{selected}
		} else {
			releases, err = client.Discover(ctx, string(repo))
		}
	} else {
		releases, err = client.Discover(ctx, string(repo))
	}
	if err != nil {
		failure := researchFailure(err)
		failure.Result = errorResearchResult(repo, failure)
		return failure.Result, failure
	}
	release, err := selectResearchRelease(releases, options.Release)
	if err != nil {
		return ResearchResult{}, &ResearchFailure{ReasonCode: researchReason(err), Err: err}
	}
	asset, err := selectResearchAsset(release, options.Asset)
	if err != nil {
		return ResearchResult{}, &ResearchFailure{ReasonCode: researchReason(err), Err: err}
	}
	result := ResearchResult{Repository: repo, Release: release, Asset: asset, Provenance: research.EvaluateProvenance(repo, release, asset)}
	if options.Inspect {
		result.Status = "READY_FOR_REVIEW"
	}
	// A missing or unsupported upstream digest is advisory evidence. Inspection
	// can still fetch the exact HTTPS asset and compute local digests; only
	// mechanical inspection blockers should prevent review of that result.
	if !options.Inspect && result.Provenance.Verdict != research.Acceptable {
		result.Status = "BLOCKED"
	}
	verification := result.Provenance
	if options.Inspect {
		var inspection research.Inspection
		var inspectErr error
		if verification.Verdict == research.Acceptable {
			inspection, inspectErr = client.InspectAsset(ctx, asset, verification, researchFormat(asset.Name), research.TargetArchitecture(asset.Name))
		} else {
			artifact, fetchErr := client.FetchUnverified(ctx, asset)
			if fetchErr == nil {
				defer func() { _ = artifact.Cleanup() }()
				inspection, inspectErr = research.Inspect(ctx, artifact, researchFormat(asset.Name), research.TargetArchitecture(asset.Name))
			} else {
				inspectErr = fetchErr
			}
		}
		if inspectErr != nil {
			classified := classify("registry inspect", inspectErr)
			result.Status = "ERROR"
			result.Error = &ResearchError{Kind: "local_failure", ReasonCode: "INSPECTION_ERROR", Message: classified.Error()}
			return result, classified
		}
		result.Inspection = &inspection
		if len(inspection.Blockers) != 0 {
			result.Status = "BLOCKED"
		}
	}
	return result, nil
}

func researchFailure(err error) *ResearchFailure {
	failure := &ResearchFailure{ReasonCode: "API_ERROR", Err: err}
	var apiErr *research.APIError
	if errors.As(err, &apiErr) {
		failure.Kind = apiErr.Kind
		failure.HTTPStatus = apiErr.Status
		switch apiErr.Kind {
		case research.APIErrorNotFound:
			failure.ReasonCode = "REPOSITORY_NOT_FOUND"
		case research.APIErrorAuth:
			failure.ReasonCode = "AUTHENTICATION_FAILURE"
		case research.APIErrorRateLimited:
			failure.ReasonCode = "RATE_LIMITED"
		case research.APIErrorServer:
			failure.ReasonCode = "GITHUB_SERVER_FAILURE"
		case research.APIErrorMalformed:
			failure.ReasonCode = "MALFORMED_RESPONSE"
		case research.APIErrorRedirect:
			failure.ReasonCode = "REDIRECT_SECURITY_VIOLATION"
		case research.APIErrorNetwork:
			failure.ReasonCode = "NETWORK_FAILURE"
		}
	}
	return failure
}

func errorResearchResult(repo research.Repository, failure *ResearchFailure) ResearchResult {
	return ResearchResult{
		Repository: repo,
		Provenance: research.Provenance{
			Verdict:    research.Error,
			ReasonCode: failure.ReasonCode,
			Message:    failure.Err.Error(),
		},
		Status: "ERROR",
		Error:  &ResearchError{Kind: failure.Kind, HTTPStatus: failure.HTTPStatus, ReasonCode: failure.ReasonCode, Message: failure.Err.Error()},
	}
}

func selectResearchRelease(releases []research.Release, tag string) (research.Release, error) {
	if tag != "" {
		for _, r := range releases {
			if r.Tag == tag {
				return r, nil
			}
		}
		return research.Release{}, errors.New("release not found")
	}
	available := make([]research.Release, 0)
	for _, r := range releases {
		if !r.Draft {
			available = append(available, r)
		}
	}
	if len(available) == 0 {
		return research.Release{}, errors.New("release not found")
	}
	if len(available) != 1 {
		return research.Release{}, errors.New("release selection is ambiguous; specify --release")
	}
	return available[0], nil
}

func selectResearchAsset(release research.Release, name string) (research.Asset, error) {
	if name != "" {
		var selected research.Asset
		for _, a := range release.Assets {
			if a.Name == name {
				if selected.ID != 0 {
					return research.Asset{}, errors.New("asset selection is ambiguous; asset name is duplicated")
				}
				selected = a
			}
		}
		if selected.ID != 0 {
			return selected, nil
		}
		return research.Asset{}, errors.New("asset not found")
	}
	if len(release.Assets) != 1 {
		return research.Asset{}, fmt.Errorf("asset selection is ambiguous; specify --asset")
	}
	return release.Assets[0], nil
}

func researchFormat(name string) archive.Format {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".tar.gz"):
		return archive.FormatTarGz
	case strings.HasSuffix(lower, ".tar.xz"):
		return archive.FormatTarXZ
	case strings.HasSuffix(lower, ".zip"):
		return archive.FormatZip
	case strings.HasSuffix(lower, ".appimage"):
		return "appimage"
	}
	return ""
}

func containsString(values []string, needle string) bool {
	for _, v := range values {
		if v == needle {
			return true
		}
	}
	return false
}
func researchReason(err error) string {
	if strings.Contains(err.Error(), "release not found") {
		return "RELEASE_NOT_FOUND"
	}
	if strings.Contains(err.Error(), "asset not found") {
		return "ASSET_NOT_FOUND"
	}
	return "INVALID_SELECTION"
}
