package app

import (
	"testing"

	"github.com/drobilica/tarlink/internal/research"
)

func TestResearchStatusPreservesCanonicalAssessmentSemantics(t *testing.T) {
	tests := []struct {
		name       string
		assessment research.Assessment
		want       string
	}{
		{name: "ready", assessment: research.AssessmentReady, want: "READY_FOR_REVIEW"},
		{name: "needs review", assessment: research.AssessmentNeedsReview, want: "READY_FOR_REVIEW"},
		{name: "needs input", assessment: research.AssessmentNeedsInput, want: "NEEDS_INPUT"},
		{name: "blocked", assessment: research.AssessmentBlocked, want: "BLOCKED"},
		{name: "unknown fails closed", assessment: research.Assessment("unknown"), want: "NEEDS_INPUT"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := researchStatus(test.assessment); got != test.want {
				t.Fatalf("researchStatus(%q) = %q, want %q", test.assessment, got, test.want)
			}
		})
	}
}
