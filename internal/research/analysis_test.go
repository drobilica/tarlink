package research

import "testing"

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
		{"none", []Asset{{ID: 1, Name: "source.zip"}}, AssessmentBlocked, "NO_LINUX_ARTIFACT"},
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
