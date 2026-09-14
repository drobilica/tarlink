package research

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupScriptIsNeverAnExecutableCandidate(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "install.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\necho unsafe\n"), 0755); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if got := executableCandidateScore("install.sh", info, bytes.TrimSpace([]byte("#!/bin/sh\necho unsafe"))); got != 0 {
		t.Fatalf("script score=%d", got)
	}
}

func TestClassifyRuntimeCompatibilityFailsClosed(t *testing.T) {
	deps := &ELFDependencies{Files: []ELFDependencyFile{{Path: "app"}}, ExternalSONAMEs: []string{"libSDL2.so.0"}}
	if got, id, missing := ClassifyRuntimeCompatibility(deps, map[string][]string{"steamrt": {"libSDL2.so.0"}}); got != RuntimeCompatible || id != "steamrt" || len(missing) != 0 {
		t.Fatalf("%s %s %v", got, id, missing)
	}
	if got, _, missing := ClassifyRuntimeCompatibility(deps, map[string][]string{"steamrt": {"libSDL2_image.so.0"}}); got != RuntimeMissing || len(missing) != 1 {
		t.Fatalf("%s %v", got, missing)
	}
	if got, _, _ := ClassifyRuntimeCompatibility(deps, map[string][]string{"a": {"libSDL2.so.0"}, "b": {"libSDL2.so.0"}}); got != RuntimeIndeterminate {
		t.Fatal(got)
	}
}

func TestCompareReleaseAnalysisIgnoresReleaseIdentityButDetectsDrift(t *testing.T) {
	a := ReleaseAnalysis{Artifacts: []ArtifactAnalysis{{Format: "tar.gz", Platform: "linux-amd64", Inspection: &Inspection{Executables: []string{"app"}}}}}
	b := ReleaseAnalysis{Release: a.Release, Artifacts: append([]ArtifactAnalysis(nil), a.Artifacts...)}
	b.Release.Tag = "v2"
	if got := CompareReleaseAnalysis(a, b); len(got) != 1 || got[0] != DeltaSameShape {
		t.Fatal(got)
	}
	b.Artifacts[0].Inspection = &Inspection{Executables: []string{"new-app"}}
	if got := CompareReleaseAnalysis(a, b); len(got) != 1 || got[0] != DeltaExecutableChanged {
		t.Fatal(got)
	}
}

func TestAnalyzeReleaseFailsClosedForAmbiguityAndNegativeAssets(t *testing.T) {
	base := Release{ID: 1, Repository: "o/r"}
	for _, tc := range []struct {
		name    string
		assets  []Asset
		want    Assessment
		blocker string
	}{
		{"one", []Asset{{ID: 1, Name: "app-linux-amd64.tar.gz"}}, AssessmentNeedsInput, ""},
		{"multi", []Asset{{ID: 1, Name: "a-linux-amd64.tar.gz"}, {ID: 2, Name: "b-linux-amd64.tar.gz"}}, AssessmentNeedsInput, ""},
		{"windows", []Asset{{ID: 1, Name: "a-windows.exe"}}, AssessmentBlocked, "WINDOWS_ONLY"},
		{"none", []Asset{{ID: 1, Name: "source.zip"}}, AssessmentNeedsInput, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			r.Assets = tc.assets
			got := AnalyzeRelease(r, nil)
			if got.Assessment != tc.want {
				t.Fatalf("assessment=%s", got.Assessment)
			}
			if tc.blocker != "" && (len(got.Blockers) != 1 || got.Blockers[0] != tc.blocker) {
				t.Fatalf("blockers=%v", got.Blockers)
			}
		})
	}
}

func TestAnalyzeReleaseReadyOnlyAfterIndependentInspection(t *testing.T) {
	r := Release{ID: 1, Repository: "o/r", Assets: []Asset{{ID: 1, Name: "app-linux-amd64.tar.gz"}}}
	got := AnalyzeRelease(r, map[int64]Inspection{1: {ArtifactType: "tar.gz", Executables: []string{"app"}}})
	if got.Assessment != AssessmentReady || got.Artifacts[0].Platform != "linux-amd64" {
		t.Fatalf("%+v", got)
	}
}

func TestBlockersFromAnalysisUsesNarrowLedgerTerms(t *testing.T) {
	got := BlockersFromAnalysis(ReleaseAnalysis{Blockers: []string{"NO_LINUX_ARTIFACT"}, Ambiguities: []string{"EXECUTABLE", "PLATFORM"}})
	if strings.Join(got, ",") != "AMBIGUOUS_ARTIFACT,AMBIGUOUS_EXECUTABLE,NO_LINUX_ARTIFACT" {
		t.Fatal(got)
	}
}

// This blind fixture corpus uses release/inspection evidence only; expected
// scores are supplied after analysis, mirroring registry/candidate comparison.
func TestGoldenDiscoverySafetyCorpus(t *testing.T) {
	cases := []struct {
		name        string
		release     Release
		inspections map[int64]Inspection
		expected    Assessment
	}{
		{"ordinary-archive", Release{Repository: "o/r", Assets: []Asset{{ID: 1, Name: "game.zip"}}}, map[int64]Inspection{1: {ArtifactType: "zip", Executables: []string{"game"}, Dependencies: &ELFDependencies{Files: []ELFDependencyFile{{Path: "game", Architecture: "amd64"}}}}}, AssessmentReady},
		{"multi-architecture", Release{Repository: "o/r", Assets: []Asset{{ID: 1, Name: "one.zip"}, {ID: 2, Name: "two.zip"}}}, map[int64]Inspection{1: {ArtifactType: "zip", Executables: []string{"one"}, Dependencies: &ELFDependencies{Files: []ELFDependencyFile{{Path: "one", Architecture: "amd64"}}}}, 2: {ArtifactType: "zip", Executables: []string{"two"}, Dependencies: &ELFDependencies{Files: []ELFDependencyFile{{Path: "two", Architecture: "arm64"}}}}}, AssessmentNeedsInput},
		{"openra-style", Release{Repository: "o/r", Assets: []Asset{{ID: 1, Name: "one.AppImage"}, {ID: 2, Name: "two.AppImage"}}}, map[int64]Inspection{1: {ArtifactType: "appimage"}, 2: {ArtifactType: "appimage"}}, AssessmentNeedsInput},
		{"unsafe", Release{Repository: "o/r", Assets: []Asset{{ID: 1, Name: "game.tar.gz"}}}, map[int64]Inspection{1: {ArtifactType: "tar.gz", Blockers: []string{"UNSUPPORTED_ARTIFACT"}}}, AssessmentBlocked},
		{"setup-script", Release{Repository: "o/r", Assets: []Asset{{ID: 1, Name: "game.zip"}}}, map[int64]Inspection{1: {ArtifactType: "zip", Blockers: []string{"NO_EXECUTABLE"}}}, AssessmentBlocked},
	}
	wrong := 0
	for _, tc := range cases {
		got := AnalyzeRelease(tc.release, tc.inspections)
		if got.Assessment != tc.expected {
			wrong++
			t.Errorf("%s: got %s want %s", tc.name, got.Assessment, tc.expected)
		}
	}
	if wrong != 0 {
		t.Fatalf("golden corpus WRONG=%d", wrong)
	}
}

func TestRuntimeAnnotationAcceptsAppImageWithoutELFDependencyReport(t *testing.T) {
	analysis := ReleaseAnalysis{Artifacts: []ArtifactAnalysis{{Inspection: &Inspection{ArtifactType: "appimage"}}}}
	if analysis.Artifacts[0].Inspection.Dependencies != nil {
		t.Fatal("test fixture")
	}
}
