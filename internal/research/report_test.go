package research

import (
	"strings"
	"testing"
)

func reportCandidate(id, status string, blockers ...string) Candidate {
	return Candidate{ID: id, Upstream: "Owner/Repo-" + id, Status: status,
		Blockers: blockers, LastChecked: ReleaseIdentity{ReleaseTag: "v1.0 (beta)", ReleaseID: 1}}
}

func TestRenderCandidateReportGroupsAndSortsDeterministically(t *testing.T) {
	ledger := CandidateLedger{Candidates: []Candidate{
		reportCandidate("z-ready", "ready"),
		reportCandidate("f1-race", "blocked", "MISSING_RUNTIME_LIBS"),
		reportCandidate("win", "blocked", "WINDOWS_ONLY"),
		reportCandidate("later", "deferred"),
		reportCandidate("old", "rejected", "SYSTEM_INTEGRATION_REQUIRED"),
		reportCandidate("a-ready", "ready"),
	}}
	first := RenderCandidateReport(ledger)
	second := RenderCandidateReport(ledger)
	if first != second {
		t.Fatal("report is not deterministic")
	}
	for _, section := range []string{"Ready / Potential candidates", "Deferred / Needs investigation", "Missing runtime libraries", "Windows only", "Rejected / Out of scope"} {
		if !strings.Contains(first, "## "+section) {
			t.Fatalf("missing section %q: %s", section, first)
		}
	}
	if strings.Contains(first, "## No Linux artifact") {
		t.Fatal("empty section emitted")
	}
	if strings.Index(first, "| a-ready |") > strings.Index(first, "| z-ready |") {
		t.Fatal("candidates not sorted by id")
	}
}

func TestRenderCandidateReportMultiBlockerAndEvidence(t *testing.T) {
	candidate := reportCandidate("multi", "blocked", "WINDOWS_ONLY", "MISSING_RUNTIME_LIBS")
	candidate.EvidenceLinks = []EvidenceLink{{Label: "a]\\b", URL: "https://example.test/a_(b)"}}
	candidate.Notes = "pipe | newline\nangle < > & backtick `"
	got := RenderCandidateReport(CandidateLedger{Candidates: []Candidate{candidate}})
	if !strings.Contains(got, "## Missing runtime libraries") || strings.Contains(got, "## Windows only") {
		t.Fatalf("primary grouping is not deterministic: %s", got)
	}
	if !strings.Contains(got, "WINDOWS_ONLY") || !strings.Contains(got, `[a\]\\b](https://example.test/a_%28b%29)`) {
		t.Fatalf("blocker/evidence missing: %s", got)
	}
	if strings.Contains(got, "pipe | newline") || !strings.Contains(got, `pipe \| newline<br>`) {
		t.Fatalf("markdown table escaping failed: %s", got)
	}
}

func TestRenderCandidateReportHeaderHasNoTimestamp(t *testing.T) {
	got := RenderCandidateReport(CandidateLedger{})
	if !strings.Contains(got, "Generated from `registry-research/candidates.yaml`.") || strings.Contains(got, "Generated:") {
		t.Fatalf("invalid report header: %s", got)
	}
}
