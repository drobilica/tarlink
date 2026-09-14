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
	AssessmentReady      Assessment = "ready"
	AssessmentNeedsInput Assessment = "needs-input"
	AssessmentBlocked    Assessment = "blocked"
)

// ArtifactAnalysis is static, execution-free evidence for one release asset.
// Inspection is retained verbatim so callers do not re-parse archives or ELF
// data in separate lifecycle-specific paths.
type ArtifactAnalysis struct {
	Asset       Asset       `json:"asset"`
	Platform    string      `json:"platform,omitempty"`
	Format      string      `json:"format,omitempty"`
	Inspection  *Inspection `json:"inspection,omitempty"`
	Blockers    []string    `json:"blockers,omitempty"`
	Ambiguities []string    `json:"ambiguities,omitempty"`
}

// ReleaseAnalysis is the canonical release/artifact evidence representation.
// It is intentionally mechanical: semantic metadata and registry policy stay
// outside this package.
type ReleaseAnalysis struct {
	Repository  Repository         `json:"repository"`
	Release     Release            `json:"release"`
	Artifacts   []ArtifactAnalysis `json:"artifacts"`
	Assessment  Assessment         `json:"assessment"`
	Blockers    []string           `json:"blockers,omitempty"`
	Ambiguities []string           `json:"ambiguities,omitempty"`
}

// AnalyzeRelease builds deterministic release evidence from independently
// collected artifact inspections. An absent inspection is evidence that still
// needs investigation, not a reason to infer safety from an asset filename.
func AnalyzeRelease(release Release, inspections map[int64]Inspection) ReleaseAnalysis {
	r := ReleaseAnalysis{Repository: release.Repository, Release: release, Assessment: AssessmentBlocked}
	assets := append([]Asset(nil), release.Assets...)
	sort.Slice(assets, func(i, j int) bool { return assets[i].Name < assets[j].Name })
	linuxNamed, windowsNamed, supported := 0, 0, 0
	for _, asset := range assets {
		name := strings.ToLower(asset.Name)
		platform := InferPlatform(asset.Name)
		looksLinux := platform != "" || strings.Contains(name, "linux") || strings.HasSuffix(name, ".appimage")
		if strings.Contains(name, "windows") || strings.HasSuffix(name, ".exe") || strings.HasSuffix(name, ".msi") {
			windowsNamed++
		}
		if !looksLinux {
			continue
		}
		linuxNamed++
		a := ArtifactAnalysis{Asset: asset, Platform: platform, Format: artifactFormat(asset.Name)}
		if a.Format == "" {
			a.Blockers = append(a.Blockers, "UNSUPPORTED_ARTIFACT")
		} else {
			supported++
		}
		if platform == "" {
			a.Ambiguities = append(a.Ambiguities, "PLATFORM")
		}
		if in, ok := inspections[asset.ID]; ok {
			copy := in
			a.Inspection = &copy
			a.Format = in.ArtifactType
			a.Blockers = append(a.Blockers, in.Blockers...)
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
	if linuxNamed == 0 {
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
	for _, a := range r.Artifacts {
		r.Blockers = append(r.Blockers, a.Blockers...)
		r.Ambiguities = append(r.Ambiguities, a.Ambiguities...)
	}
	r.Blockers = uniqueAnalysisStrings(r.Blockers)
	r.Ambiguities = uniqueAnalysisStrings(r.Ambiguities)
	if len(r.Blockers) != 0 {
		return r
	}
	if len(r.Artifacts) != 1 || len(r.Ambiguities) != 0 {
		r.Assessment = AssessmentNeedsInput
		return r
	}
	r.Assessment = AssessmentReady
	return r
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
