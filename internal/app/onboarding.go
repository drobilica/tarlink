package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/drobilica/tarlink/internal/archive"
	"github.com/drobilica/tarlink/internal/manifest"
	"github.com/drobilica/tarlink/internal/research"
	"go.yaml.in/yaml/v3"
)

// RegistryInspectOptions selects one local manifest, a bounded local tree, or
// an exact GitHub release-asset URL. It is advisory: registry validate remains
// the authoritative structural validation command.
type RegistryInspectOptions struct {
	Target  string
	Refresh bool
}

type RegistryManifestInspection struct {
	Path     string   `json:"path"`
	ID       string   `json:"id,omitempty"`
	Valid    bool     `json:"valid"`
	Checks   []string `json:"checks,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
	Error    string   `json:"error,omitempty"`
}

type RegistryDirectoryInspection struct {
	Manifests int                          `json:"manifests"`
	Valid     int                          `json:"valid"`
	Warnings  int                          `json:"warnings"`
	Invalid   int                          `json:"invalid"`
	Results   []RegistryManifestInspection `json:"results"`
}

// RegistryCandidate separates mechanical facts from unresolved maintainer
// decisions. It is shared by inspect and add so their answers cannot drift.
type RegistryCandidate struct {
	Repository       string   `json:"repository,omitempty"`
	Release          string   `json:"release,omitempty"`
	Asset            string   `json:"asset,omitempty"`
	URL              string   `json:"url,omitempty"`
	Platform         string   `json:"platform,omitempty"`
	Archive          string   `json:"archive,omitempty"`
	SHA256           string   `json:"sha256,omitempty"`
	Executable       string   `json:"executable,omitempty"`
	Icon             string   `json:"icon,omitempty"`
	RemoteIconURL    string   `json:"remote_icon_url,omitempty"`
	RemoteIconSHA256 string   `json:"remote_icon_sha256,omitempty"`
	Executables      []string `json:"executable_candidates,omitempty"`
	Icons            []string `json:"icon_candidates,omitempty"`
	Nested           []string `json:"nested_archive_candidates,omitempty"`
}

type RegistryRequiredInput struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

type RegistryInspectionResult struct {
	Status    string                       `json:"status"`
	Candidate *RegistryCandidate           `json:"candidate,omitempty"`
	Required  []RegistryRequiredInput      `json:"required,omitempty"`
	Manifest  *RegistryManifestInspection  `json:"manifest,omitempty"`
	Directory *RegistryDirectoryInspection `json:"directory,omitempty"`
}

type RegistryAddOptions struct {
	Target         string
	Refresh        bool
	NonInteractive bool
	ID             string
	Name           string
	Summary        string
	Categories     []string
	CreateBinLink  *bool
}

type RegistryAddResult struct {
	Status    string                  `json:"status"`
	Candidate RegistryCandidate       `json:"resolved"`
	Required  []RegistryRequiredInput `json:"required,omitempty"`
	Manifest  *manifest.Document      `json:"manifest,omitempty"`
	YAML      []byte                  `json:"-"`
}

// RegistryOnboardingService is maintainer-only and never changes an official
// registry. It deliberately lives outside the operational Service interface.
type RegistryOnboardingService interface {
	InspectRegistry(context.Context, RegistryInspectOptions) (RegistryInspectionResult, error)
	AddRegistry(context.Context, RegistryAddOptions) (RegistryAddResult, error)
}

func (m *Maintainer) InspectRegistry(ctx context.Context, options RegistryInspectOptions) (RegistryInspectionResult, error) {
	if strings.TrimSpace(options.Target) == "" {
		return RegistryInspectionResult{}, errors.New("inspection target is required")
	}
	if target, err := research.ParseReleaseAssetURL(options.Target); err == nil {
		candidate, required, err := m.deriveRegistryCandidate(ctx, target, options.Refresh)
		if err != nil {
			return RegistryInspectionResult{}, err
		}
		status := "ready"
		if len(required) != 0 {
			status = "needs-input"
		}
		return RegistryInspectionResult{Status: status, Candidate: &candidate, Required: required}, nil
	}
	info, err := os.Lstat(options.Target)
	if err != nil {
		return RegistryInspectionResult{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return RegistryInspectionResult{}, errors.New("inspection target must not be a symlink")
	}
	if info.Mode().IsRegular() {
		value := inspectManifest(options.Target, options.Target)
		status := "ready"
		if !value.Valid {
			status = "invalid"
		}
		return RegistryInspectionResult{Status: status, Manifest: &value}, nil
	}
	if !info.IsDir() {
		return RegistryInspectionResult{}, errors.New("inspection target must be a GitHub release asset, manifest, or directory")
	}
	directory, err := inspectDirectory(options.Target)
	if err != nil {
		return RegistryInspectionResult{}, err
	}
	status := "ready"
	if directory.Invalid != 0 {
		status = "invalid"
	}
	return RegistryInspectionResult{Status: status, Directory: &directory}, nil
}

func (m *Maintainer) AddRegistry(ctx context.Context, options RegistryAddOptions) (RegistryAddResult, error) {
	target, err := research.ParseReleaseAssetURL(options.Target)
	if err != nil {
		return RegistryAddResult{}, errors.New("registry add requires an exact GitHub release asset URL")
	}
	candidate, required, err := m.deriveRegistryCandidate(ctx, target, options.Refresh)
	if err != nil {
		return RegistryAddResult{}, err
	}
	if len(required) != 0 {
		if len(options.Categories) == 0 {
			required = append(required, RegistryRequiredInput{Field: "categories", Reason: "semantic_category_required"})
		}
		return RegistryAddResult{Status: "needs-input", Candidate: candidate, Required: uniqueRequired(required)}, nil
	}
	return CompleteRegistryCandidate(candidate, options)
}

// CompleteRegistryCandidate applies reviewed maintainer decisions to a
// previously derived candidate. It performs no network or filesystem access.
func CompleteRegistryCandidate(candidate RegistryCandidate, options RegistryAddOptions) (RegistryAddResult, error) {
	if options.ID == "" {
		options.ID = defaultID(candidate.Repository)
	}
	if options.Name == "" {
		options.Name = defaultName(candidate.Repository)
	}
	if options.Summary == "" {
		options.Summary = "Portable Linux release of " + options.Name
	}
	var required []RegistryRequiredInput
	if len(options.Categories) == 0 {
		required = append(required, RegistryRequiredInput{Field: "categories", Reason: "semantic_category_required"})
	}
	if candidate.Executable != "" && options.CreateBinLink == nil && (contains(options.Categories, "games") || contains(options.Categories, "recompilation")) {
		required = append(required, RegistryRequiredInput{Field: "create-bin-link", Reason: "semantic_bin_link_policy_required"})
	}
	if len(required) != 0 {
		return RegistryAddResult{Status: "needs-input", Candidate: candidate, Required: uniqueRequired(required)}, nil
	}
	document, err := candidateDocument(candidate, options)
	if err != nil {
		return RegistryAddResult{}, err
	}
	yamlBytes, err := yaml.Marshal(document)
	if err != nil {
		return RegistryAddResult{}, err
	}
	if _, err := manifest.ParseBytes(yamlBytes); err != nil {
		return RegistryAddResult{}, fmt.Errorf("generated manifest: %w", err)
	}
	return RegistryAddResult{Status: "ready", Candidate: candidate, Manifest: document, YAML: yamlBytes}, nil
}

func (m *Maintainer) deriveRegistryCandidate(ctx context.Context, target research.ReleaseAssetTarget, refresh bool) (RegistryCandidate, []RegistryRequiredInput, error) {
	client := &research.Client{CacheRoot: filepath.Join(m.layout.Cache, "registry-research"), Refresh: refresh}
	if m.client != nil {
		client.HTTP = m.client.HTTP
	}
	resolved, err := client.ResolveReleaseAsset(ctx, target)
	if err != nil {
		return RegistryCandidate{}, nil, err
	}
	release, asset := resolved.Release, resolved.Asset
	provenance := research.EvaluateProvenance(target.Repository, release, asset)
	format := archive.Format("")
	if strings.HasSuffix(strings.ToLower(asset.Name), ".appimage") {
		format = "appimage"
	}
	var inspection research.Inspection
	if provenance.Verdict == research.Acceptable {
		inspection, err = client.InspectAsset(ctx, asset, provenance, format, research.TargetArchitecture(asset.Name))
	} else {
		artifact, fetchErr := client.FetchUnverified(ctx, asset)
		if fetchErr != nil {
			return RegistryCandidate{}, nil, fetchErr
		}
		defer artifact.Cleanup()
		inspection, err = research.Inspect(ctx, artifact, format, research.TargetArchitecture(asset.Name))
	}
	if err != nil {
		return RegistryCandidate{}, nil, err
	}
	candidate := RegistryCandidate{Repository: string(target.Repository), Release: release.Tag, Asset: asset.Name, URL: target.URL, Platform: research.InferPlatform(asset.Name), Archive: inspection.ArtifactType, SHA256: inspection.ComputedDigests["sha256"], Executables: append([]string(nil), inspection.Executables...), Icons: append([]string(nil), inspection.Icons...), Nested: append([]string(nil), inspection.Nested...)}
	if candidate.Archive == "appimage" {
		candidate.Executable = "appimage"
		candidate.Executables = []string{"appimage"}
	}
	sort.Strings(candidate.Executables)
	sort.Strings(candidate.Icons)
	sort.Strings(candidate.Nested)
	var required []RegistryRequiredInput
	if candidate.Platform == "" {
		required = append(required, RegistryRequiredInput{Field: "platform", Reason: "ambiguous_platform"})
	}
	if candidate.Archive == "" || candidate.Archive == "unknown" {
		required = append(required, RegistryRequiredInput{Field: "archive", Reason: "unsupported_artifact"})
	}
	if candidate.SHA256 == "" {
		return RegistryCandidate{}, nil, errors.New("local SHA-256 calculation failed")
	}
	if candidate.Executable != "" {
		// AppImages have one fixed opaque runtime path; archive candidates have
		// already been selected only when their static evidence is unique.
	} else if len(candidate.Executables) == 1 {
		candidate.Executable = candidate.Executables[0]
	} else if len(candidate.Executables) == 0 {
		required = append(required, RegistryRequiredInput{Field: "executable", Reason: "no_executable_candidate"})
	} else {
		required = append(required, RegistryRequiredInput{Field: "executable", Reason: "ambiguous_executables"})
	}
	if len(candidate.Icons) == 1 {
		candidate.Icon = candidate.Icons[0]
	} else if len(candidate.Icons) > 1 {
		required = append(required, RegistryRequiredInput{Field: "icon", Reason: "ambiguous_icons"})
	}
	if candidate.Icon == "" && len(candidate.Icons) == 0 {
		if files, iconErr := client.DiscoverRepositoryIconCandidates(ctx, string(target.Repository), release.Tag); iconErr == nil && len(files) == 1 {
			if data, fetchErr := client.FetchRepositoryFile(ctx, files[0]); fetchErr == nil {
				digest := sha256.Sum256(data)
				candidate.RemoteIconURL = files[0].URL
				candidate.RemoteIconSHA256 = hex.EncodeToString(digest[:])
			}
		}
	}
	if len(candidate.Nested) != 0 {
		required = append(required, RegistryRequiredInput{Field: "nested-archive", Reason: "nested_archive_requires_review"})
	}
	for _, blocker := range inspection.Blockers {
		if blocker == "UNSUPPORTED_ARTIFACT" || blocker == "UNSUPPORTED_ARCH" {
			required = append(required, RegistryRequiredInput{Field: "artifact", Reason: strings.ToLower(blocker)})
		}
	}
	return candidate, uniqueRequired(required), nil
}

func inspectManifest(path, display string) RegistryManifestInspection {
	result := RegistryManifestInspection{Path: display}
	f, err := os.Open(path)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer f.Close()
	document, err := manifest.Parse(io.LimitReader(f, manifest.MaxManifestBytes+1))
	if err != nil {
		result.Error = err.Error()
		return result
	}
	result.Valid, result.ID = true, document.ID
	result.Checks = append(result.Checks, "schema v5")
	platforms := make([]string, 0, len(document.Platforms))
	for platform := range document.Platforms {
		platforms = append(platforms, platform)
	}
	sort.Strings(platforms)
	result.Checks = append(result.Checks, platforms...)
	if len(platforms) != 0 {
		if release, err := document.Platforms[platforms[0]].ReleaseHistory.ResolveDefault(); err == nil {
			result.Checks = append(result.Checks, "release "+release.Version, "archive "+release.Archive)
		}
	}
	result.Checks = append(result.Checks, strings.ToUpper(document.Release.Verification.Algorithm)+" declared", "executable declared")
	if document.Desktop != nil {
		result.Checks = append(result.Checks, "desktop integration declared")
		if document.Desktop.Icon != nil && document.Desktop.Icon.Path != "" {
			result.Checks = append(result.Checks, "archive icon declared")
		}
	}
	return result
}

func inspectDirectory(root string) (RegistryDirectoryInspection, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return RegistryDirectoryInspection{}, err
	}
	var paths []string
	err = filepath.WalkDir(absolute, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == absolute {
			return nil
		}
		rel, err := filepath.Rel(absolute, path)
		if err != nil {
			return err
		}
		depth := len(strings.Split(rel, string(os.PathSeparator)))
		if entry.IsDir() {
			if entry.Type()&os.ModeSymlink != 0 || depth > 2 {
				return filepath.SkipDir
			}
			return nil
		}
		if depth <= 3 && entry.Name() == "manifest.yaml" && entry.Type().IsRegular() {
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return RegistryDirectoryInspection{}, err
	}
	sort.Strings(paths)
	result := RegistryDirectoryInspection{Manifests: len(paths), Results: make([]RegistryManifestInspection, 0, len(paths))}
	for _, path := range paths {
		rel, _ := filepath.Rel(absolute, path)
		value := inspectManifest(path, filepath.ToSlash(rel))
		result.Results = append(result.Results, value)
		if value.Valid {
			result.Valid++
		} else {
			result.Invalid++
		}
	}
	return result, nil
}

func candidateDocument(candidate RegistryCandidate, options RegistryAddOptions) (*manifest.Document, error) {
	if candidate.Platform == "" || candidate.Archive == "" || candidate.Executable == "" {
		return nil, errors.New("candidate has unresolved required fields")
	}
	exeName := filepath.Base(candidate.Executable)
	path := candidate.Executable
	if candidate.Archive == "appimage" {
		path = "appimage"
	}
	document := &manifest.Document{
		Schema: manifest.CurrentSchema, ID: options.ID, Name: options.Name,
		Summary: options.Summary, Homepage: "https://github.com/" + candidate.Repository,
		Categories: append([]string(nil), options.Categories...),
		Release: manifest.ReleaseDocument{
			Current: candidate.Release, Archive: candidate.Archive,
			Verification: manifest.ReleaseVerification{Algorithm: "sha256"},
			Releases: []manifest.ReleaseDefinition{{
				Version: candidate.Release,
				Artifacts: map[string]manifest.Artifact{
					candidate.Platform: {URL: candidate.URL, Verification: manifest.ArtifactVerification{Digest: candidate.SHA256, Source: candidate.URL}},
				},
			}},
		},
		Application: manifest.ApplicationDefinition{Executable: &manifest.ExecutableDefinition{Name: exeName, Path: path, CreateBinLink: options.CreateBinLink}},
	}
	if candidate.Icon != "" && candidate.Archive != "appimage" {
		document.Desktop = &manifest.DesktopDefinition{Executable: exeName, Categories: desktopCategories(options.Categories), Icon: &manifest.DesktopIcon{Path: candidate.Icon}}
	} else if candidate.RemoteIconURL != "" {
		document.Desktop = &manifest.DesktopDefinition{Executable: exeName, Categories: desktopCategories(options.Categories), Icon: &manifest.DesktopIcon{URL: candidate.RemoteIconURL, SHA256: candidate.RemoteIconSHA256}}
	}
	return document, nil
}

func defaultID(repository string) string {
	part := repository
	if i := strings.LastIndex(part, "/"); i >= 0 {
		part = part[i+1:]
	}
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(part) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
func defaultName(repository string) string {
	part := repository
	if i := strings.LastIndex(part, "/"); i >= 0 {
		part = part[i+1:]
	}
	return strings.ReplaceAll(strings.ReplaceAll(part, "-", " "), "_", " ")
}
func desktopCategories(categories []string) []string {
	for _, value := range categories {
		switch value {
		case "development", "game-development":
			return []string{"Development"}
		case "emulation":
			return []string{"Emulator"}
		case "games", "recompilation":
			return []string{"Game"}
		case "graphics":
			return []string{"Graphics"}
		}
	}
	return []string{"Utility"}
}
func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
func uniqueRequired(values []RegistryRequiredInput) []RegistryRequiredInput {
	sort.Slice(values, func(i, j int) bool {
		if values[i].Field == values[j].Field {
			return values[i].Reason < values[j].Reason
		}
		return values[i].Field < values[j].Field
	})
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}
