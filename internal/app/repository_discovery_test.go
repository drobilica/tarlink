package app_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/drobilica/tarlink/internal/app"
)

func TestRepositoryInspectFailsClosedForMultipleLinuxArtifacts(t *testing.T) {
	m := newResearchMaintainer(t, func(r *http.Request) *http.Response {
		return jsonResponse(http.StatusOK, researchReleaseJSON(researchAssetJSON(20, "a-linux-amd64.tar.gz", "")+","+researchAssetJSON(21, "b-linux-amd64.tar.gz", "")))
	})
	result, err := m.Research(context.Background(), app.ResearchOptions{Repository: "owner/repo", Inspect: true})
	if err != nil || result.Status != "NEEDS_INPUT" || result.Analysis == nil || len(result.Analysis.Artifacts) != 2 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}
