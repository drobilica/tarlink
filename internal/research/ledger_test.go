package research

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func validLedger() CandidateLedger {
	return CandidateLedger{Candidates: []Candidate{{ID: "demo", Upstream: "Owner/Repo", Status: "blocked", LastChecked: ReleaseIdentity{ReleaseTag: "v1", ReleaseID: 1}, Blockers: []string{"NESTED_ARCHIVE_UNSUPPORTED"}, ReconsiderWhen: []string{"new-upstream-release", "capability:nested-archive"}}}}
}
func TestValidateLedger(t *testing.T) {
	l := validLedger()
	if err := ValidateLedger(l); err != nil {
		t.Fatal(err)
	}
	l.Candidates[0].ID = "bad_id"
	if err := ValidateLedger(l); err == nil {
		t.Fatal("expected invalid id")
	}
	l = validLedger()
	l.Candidates[0].Blockers = []string{"UNKNOWN"}
	if err := ValidateLedger(l); err == nil {
		t.Fatal("expected unknown blocker")
	}
	l = validLedger()
	l.Candidates[0].ReconsiderWhen = []string{"capability:bogus"}
	if err := ValidateLedger(l); err == nil {
		t.Fatal("expected unknown capability")
	}
}
func TestLoadLedger(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "candidates.yaml")
	if err := os.WriteFile(p, []byte("candidates:\n  - id: demo\n    upstream: Owner/Repo\n    status: deferred\n    last_checked:\n      release_tag: v1\n      release_id: 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	l, e := LoadLedger(p)
	if e != nil || len(l.Candidates) != 1 {
		t.Fatalf("ledger=%+v err=%v", l, e)
	}
}

func TestProjectCandidateLedger(t *testing.T) {
	ledger, err := LoadLedger(filepath.Join("..", "..", "registry-research", "candidates.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Candidates) == 0 {
		t.Fatal("project candidate ledger is empty")
	}
}
func TestAnalyzeCapability(t *testing.T) {
	r, e := AnalyzeCapability(validLedger(), "nested-archive")
	if e != nil || len(r) != 1 || !r[0].FullyUnlocked {
		t.Fatalf("result=%+v err=%v", r, e)
	}
	l := validLedger()
	l.Candidates[0].Blockers = append(l.Candidates[0].Blockers, "NO_EXECUTABLE")
	r, e = AnalyzeCapability(l, "nested-archive")
	if e != nil || r[0].FullyUnlocked {
		t.Fatalf("result=%+v err=%v", r, e)
	}
}
func TestDetectChangesUsesReleaseID(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Status: "200 OK", Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`[{"id":2,"tag_name":"v1","assets":[]}]`)), Request: r}, nil
	})
	c := &Client{HTTP: &http.Client{Transport: transport}, APIBase: "https://api.test", Refresh: true}
	v := DetectChanges(context.Background(), c, validLedger())
	if v.Summary["RECHECK"] != 1 || v.Results[0].Reason != "RECREATED_RELEASE" {
		t.Fatalf("%+v", v)
	}
}

func TestDetectChangesManualDoesNotDiscover(t *testing.T) {
	called := false
	c := &Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { called = true; return nil, context.Canceled })}, APIBase: "https://api.test", Refresh: true}
	l := validLedger()
	l.Candidates[0].ReconsiderWhen = []string{"manual"}
	v := DetectChanges(context.Background(), c, l)
	if called || v.Summary["UNCHANGED"] != 1 || v.Summary["ERROR"] != 0 {
		t.Fatalf("called=%v changes=%+v", called, v)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
