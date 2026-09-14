package research

import (
	"sort"
	"strings"
)

// Assessment is the deliberately small outcome vocabulary used by every
// maintainer discovery consumer. It is advisory evidence, never registry
// approval: manifest validation remains the authority for an installable app.
type Assessment string

const (
	AssessmentReady       Assessment = "ready"
	AssessmentNeedsInput  Assessment = "needs-input"
	AssessmentBlocked     Assessment = "blocked"
	AssessmentNeedsReview Assessment = "needs-review"
)

// ArtifactAnalysis is static, execution-free evidence for one release asset.
// Inspection is retained verbatim so callers do not re-parse archives or ELF
// data in separate lifecycle-specific paths.
type ArtifactAnalysis struct {
	Asset            Asset                `json:"asset"`
	Platform         string               `json:"platform,omitempty"`
	Format           string               `json:"format,omitempty"`
	Inspection       *Inspection          `json:"inspection,omitempty"`
	Blockers         []string             `json:"blockers,omitempty"`
	Ambiguities      []string             `json:"ambiguities,omitempty"`
	Runtime          RuntimeCompatibility `json:"runtime,omitempty"`
	RuntimeID        string               `json:"runtime_id,omitempty"`
	MissingLibraries []string             `json:"missing_libraries,omitempty"`
}

// ReleaseAnalysis is the canonical release/artifact evidence representation.
// It is intentionally mechanical: semantic metadata and registry policy stay
// outside this package.
type ReleaseAnalysis struct {
	Repository       Repository           `json:"repository"`
	Release          Release              `json:"release"`
	Artifacts        []ArtifactAnalysis   `json:"artifacts"`
	Assessment       Assessment           `json:"assessment"`
	Blockers         []string             `json:"blockers,omitempty"`
	Ambiguities      []string             `json:"ambiguities,omitempty"`
	Runtime          RuntimeCompatibility `json:"runtime,omitempty"`
	RuntimeID        string               `json:"runtime_id,omitempty"`
	MissingLibraries []string             `json:"missing_libraries,omitempty"`
}

type RuntimeCompatibility string

const (
	RuntimeSelfContained RuntimeCompatibility = "self-contained"
	RuntimeNative        RuntimeCompatibility = "native"
	RuntimeCompatible    RuntimeCompatibility = "compatible"
	RuntimeMissing       RuntimeCompatibility = "missing-runtime-libraries"
	RuntimeUnsupported   RuntimeCompatibility = "unsupported"
	RuntimeIndeterminate RuntimeCompatibility = "indeterminate"
)

// ClassifyRuntimeCompatibility compares only statically observed external
// SONAMEs to exact admitted immutable runtime closures supplied by the
// registry consumer. It never probes the host, executes a binary, or solves
// distribution packages.
func ClassifyRuntimeCompatibility(deps *ELFDependencies, admitted map[string][]string) (RuntimeCompatibility, string, []string) {
	if deps == nil {
		return RuntimeIndeterminate, "", nil
	}
	if len(deps.Files) == 0 {
		return RuntimeIndeterminate, "", nil
	}
	if len(deps.ExternalSONAMEs) == 0 {
		return RuntimeSelfContained, "", nil
	}
	if len(admitted) == 0 {
		return RuntimeIndeterminate, "", nil
	}
	ids := make([]string, 0, len(admitted))
	for id := range admitted {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var compatible []string
	for _, id := range ids {
		closure := map[string]bool{}
		for _, soname := range admitted[id] {
			closure[soname] = true
		}
		missing := []string{}
		for _, soname := range deps.ExternalSONAMEs {
			if !closure[soname] {
				missing = append(missing, soname)
			}
		}
		if len(missing) == 0 {
			compatible = append(compatible, id)
		}
	}
	if len(compatible) == 1 {
		return RuntimeCompatible, compatible[0], nil
	}
	if len(compatible) > 1 {
		return RuntimeIndeterminate, "", nil
	}
	return RuntimeMissing, "", append([]string(nil), deps.ExternalSONAMEs...)
}

type ReleaseDelta string

const (
	DeltaSameShape           ReleaseDelta = "SAME_SHAPE"
	DeltaArtifactChanged     ReleaseDelta = "ARTIFACT_CHANGED"
	DeltaPackagingChanged    ReleaseDelta = "PACKAGING_CHANGED"
	DeltaExecutableChanged   ReleaseDelta = "EXECUTABLE_CHANGED"
	DeltaPlatformChanged     ReleaseDelta = "PLATFORM_CHANGED"
	DeltaNewBlocker          ReleaseDelta = "NEW_BLOCKER"
	DeltaAmbiguous           ReleaseDelta = "AMBIGUOUS"
	DeltaLayoutChanged       ReleaseDelta = "LAYOUT_CHANGED"
	DeltaRequirementsChanged ReleaseDelta = "REQUIREMENTS_CHANGED"
	DeltaBlockerChanged      ReleaseDelta = "BLOCKER_CHANGED"
	DeltaNeedsReview         ReleaseDelta = "NEEDS_REVIEW"
)

// CompareReleaseAnalysis intentionally ignores release identifiers, URLs, and
// digests. It compares only material static shape so routine upstream rebuilds
// do not masquerade as packaging drift.
func CompareReleaseAnalysis(previous, current ReleaseAnalysis) []ReleaseDelta {
	if previous.Assessment == AssessmentNeedsInput || previous.Assessment == AssessmentNeedsReview || current.Assessment == AssessmentNeedsInput || current.Assessment == AssessmentNeedsReview {
		return []ReleaseDelta{DeltaNeedsReview}
	}
	if strings.Join(previous.Blockers, "\x00") != strings.Join(current.Blockers, "\x00") {
		return []ReleaseDelta{DeltaBlockerChanged}
	}
	if len(previous.Artifacts) != len(current.Artifacts) {
		return []ReleaseDelta{DeltaArtifactChanged}
	}
	var changes []ReleaseDelta
	for i := range previous.Artifacts {
		a, b := previous.Artifacts[i], current.Artifacts[i]
		if a.Format != b.Format {
			changes = append(changes, DeltaPackagingChanged)
		}
		if a.Platform != b.Platform {
			changes = append(changes, DeltaPlatformChanged)
		}
		if executableShape(a.Inspection) != executableShape(b.Inspection) {
			changes = append(changes, DeltaExecutableChanged)
		}
		if layoutShape(a.Inspection) != layoutShape(b.Inspection) {
			changes = append(changes, DeltaLayoutChanged)
		}
		if requirementShape(a.Inspection) != requirementShape(b.Inspection) {
			changes = append(changes, DeltaRequirementsChanged)
		}
	}
	changes = uniqueDeltas(changes)
	if len(changes) == 0 {
		return []ReleaseDelta{DeltaSameShape}
	}
	return changes
}
func layoutShape(i *Inspection) string {
	if i == nil {
		return ""
	}
	return strings.Join(i.Nested, "\x00")
}
func requirementShape(i *Inspection) string {
	if i == nil || i.Dependencies == nil {
		return ""
	}
	return strings.Join(i.Dependencies.ExternalSONAMEs, "\x00")
}
func executableShape(i *Inspection) string {
	if i == nil {
		return ""
	}
	return strings.Join(i.Executables, "\x00")
}
func uniqueDeltas(in []ReleaseDelta) []ReleaseDelta {
	seen := map[ReleaseDelta]bool{}
	for _, v := range in {
		seen[v] = true
	}
	out := make([]ReleaseDelta, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// AnalyzeRelease builds deterministic release evidence from independently
// collected artifact inspections. An absent inspection is evidence that still
// needs investigation, not a reason to infer safety from an asset filename.
func AnalyzeRelease(release Release, inspections map[int64]Inspection) ReleaseAnalysis {
	r := ReleaseAnalysis{Repository: release.Repository, Release: release, Assessment: AssessmentBlocked}
	assets := append([]Asset(nil), release.Assets...)
	sort.Slice(assets, func(i, j int) bool { return assets[i].Name < assets[j].Name })
	portable, windowsNamed, supported := 0, 0, 0
	for _, asset := range assets {
		name := strings.ToLower(asset.Name)
		platform := InferPlatform(asset.Name)
		if strings.Contains(name, "windows") || strings.HasSuffix(name, ".exe") || strings.HasSuffix(name, ".msi") {
			windowsNamed++
		}
		// A portable archive is eligible for bounded static inspection even when
		// its name is generic. Filename evidence may prioritize it, but can
		// never overrule the archive/ELF evidence collected below.
		format := artifactFormat(asset.Name)
		if format == "" {
			continue
		}
		portable++
		a := ArtifactAnalysis{Asset: asset, Platform: platform, Format: format}
		supported++
		if in, ok := inspections[asset.ID]; ok {
			copy := in
			a.Inspection = &copy
			a.Format = in.ArtifactType
			a.Blockers = append(a.Blockers, in.Blockers...)
			if derived := platformFromInspection(in); derived != "" {
				if platform != "" && platform != derived {
					a.Blockers = append(a.Blockers, "UNSUPPORTED_ARCH")
				}
				platform = derived
				a.Platform = derived
			}
			if a.Platform == "" {
				a.Ambiguities = append(a.Ambiguities, "PLATFORM")
			}
			if len(in.Executables) > 1 {
				a.Ambiguities = append(a.Ambiguities, "EXECUTABLE")
			}
			if len(in.Nested) > 0 {
				a.Ambiguities = append(a.Ambiguities, "NESTED_ARCHIVE")
			}
		} else {
			a.Ambiguities = append(a.Ambiguities, "INSPECTION")
		}
		r.Artifacts = append(r.Artifacts, a)
	}
	if portable == 0 {
		if windowsNamed > 0 {
			r.Blockers = []string{"WINDOWS_ONLY"}
		} else {
			r.Blockers = []string{"NO_LINUX_ARTIFACT"}
		}
		return r
	}
	if supported == 0 {
		r.Blockers = []string{"UNSUPPORTED_ARTIFACT"}
		return r
	}
	// An unrelated source archive must not block a valid portable artifact.
	// Only candidates with no mechanical blocker participate in selection.
	viable := 0
	for _, a := range r.Artifacts {
		if len(a.Blockers) == 0 {
			viable++
			r.Ambiguities = append(r.Ambiguities, a.Ambiguities...)
		}
	}
	if viable == 0 {
		for _, a := range r.Artifacts {
			r.Blockers = append(r.Blockers, a.Blockers...)
		}
	}
	r.Blockers = uniqueAnalysisStrings(r.Blockers)
	r.Ambiguities = uniqueAnalysisStrings(r.Ambiguities)
	if len(r.Blockers) != 0 {
		return r
	}
	if viable != 1 || len(r.Ambiguities) != 0 {
		r.Assessment = AssessmentNeedsInput
		return r
	}
	r.Assessment = AssessmentReady
	return r
}

func platformFromInspection(in Inspection) string {
	if in.Dependencies == nil {
		return ""
	}
	platform := ""
	for _, executable := range in.Executables {
		for _, file := range in.Dependencies.Files {
			if file.Path != executable {
				continue
			}
			candidate := ""
			if file.Architecture == "amd64" {
				candidate = "linux-amd64"
			}
			if file.Architecture == "arm64" {
				candidate = "linux-arm64"
			}
			if candidate == "" {
				continue
			}
			if platform != "" && platform != candidate {
				return ""
			}
			platform = candidate
		}
	}
	return platform
}

func artifactFormat(name string) string {
	name = strings.ToLower(name)
	switch {
	case strings.HasSuffix(name, ".appimage"):
		return "appimage"
	case strings.HasSuffix(name, ".tar.gz"):
		return "tar.gz"
	case strings.HasSuffix(name, ".tar.xz"):
		return "tar.xz"
	case strings.HasSuffix(name, ".zip"):
		return "zip"
	default:
		return ""
	}
}

func uniqueAnalysisStrings(values []string) []string {
	set := make(map[string]bool, len(values))
	for _, v := range values {
		if v != "" {
			set[v] = true
		}
	}
	result := make([]string, 0, len(set))
	for v := range set {
		result = append(result, v)
	}
	sort.Strings(result)
	return result
}

// BlockersFromAnalysis is the sole translation from mechanical analysis into
// the durable candidate-ledger vocabulary. It does not invent policy reasons
// or mutate the ledger; maintainers record the reviewed outcome separately.
func BlockersFromAnalysis(value ReleaseAnalysis) []string {
	out := append([]string(nil), value.Blockers...)
	for _, ambiguity := range value.Ambiguities {
		switch ambiguity {
		case "EXECUTABLE":
			out = append(out, "AMBIGUOUS_EXECUTABLE")
		case "INSPECTION", "PLATFORM":
			out = append(out, "AMBIGUOUS_ARTIFACT")
		}
	}
	return uniqueAnalysisStrings(out)
}
