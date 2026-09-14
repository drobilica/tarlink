package research

import "testing"

func TestScoreBenchmarkSeparatesNeedsInputFromWrong(t *testing.T) {
	expected := BenchmarkExpectation{Release: "v1", Artifact: "a.tar.gz", Platform: "linux-amd64", Format: "tar.gz", Executable: "app", Digest: "d", Runtime: "native", Safety: "blocked"}
	_, report := ScoreBenchmark("case", expected, BenchmarkObservation{Release: "v1", Artifact: "a.tar.gz", Platform: "amd64", Format: "tar.gz", Executable: "", Digest: "d", Runtime: "native", Safety: "ready"}, true)
	if report.Wrong != 1 || report.NeedsInput != 1 || report.Equivalent != 1 || report.BlockedWrong != 1 {
		t.Fatalf("%+v", report)
	}
}
