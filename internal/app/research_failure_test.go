package app

import (
	"testing"

	"github.com/drobilica/tarlink/internal/research"
)

// A retained git tag whose release object is gone must surface a specific
// reason code so a withdrawn release is not reported as a generic API error.
func TestResearchFailureMapsTagWithoutRelease(t *testing.T) {
	failure := researchFailure(&research.APIError{
		Status:  404,
		Kind:    research.APIErrorTagWithoutRelease,
		Message: "GitHub tag \"v1\" exists but its release is not available",
	})
	if failure.Kind != research.APIErrorTagWithoutRelease || failure.ReasonCode != "TAG_WITHOUT_RELEASE" {
		t.Fatalf("failure = %+v", failure)
	}
}
