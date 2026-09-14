package research

// BenchmarkScore deliberately distinguishes a safe request for review from a
// wrong automatic conclusion. Consumers must treat WRONG as a failed safety
// benchmark, never as a tuning opportunity to guess more aggressively.
type BenchmarkScore string

const (
	BenchmarkExact      BenchmarkScore = "EXACT"
	BenchmarkEquivalent BenchmarkScore = "EQUIVALENT"
	BenchmarkNeedsInput BenchmarkScore = "NEEDS_INPUT"
	BenchmarkWrong      BenchmarkScore = "WRONG"
)

type BenchmarkExpectation struct {
	Release, Artifact, Platform, Format, Executable, Digest, Runtime, Safety string
}
type BenchmarkObservation struct {
	Release, Artifact, Platform, Format, Executable, Digest, Runtime, Safety string
}
type BenchmarkResult struct {
	ID     string                    `json:"id"`
	Fields map[string]BenchmarkScore `json:"fields"`
}
type BenchmarkReport struct {
	Results          []BenchmarkResult `json:"results"`
	Exact            int               `json:"exact"`
	Equivalent       int               `json:"equivalent"`
	NeedsInput       int               `json:"needs_input"`
	Wrong            int               `json:"wrong"`
	BlockedTotal     int               `json:"blocked_candidates_tested"`
	BlockedCorrect   int               `json:"correctly_blocked"`
	BlockedWrong     int               `json:"incorrectly_accepted"`
	BlockedAmbiguous int               `json:"blocked_ambiguous"`
}

// ScoreBenchmark compares analysis output only after it has been produced.
// The caller owns acquisition and must not provide expected values to that
// analysis pass; this pure comparator makes that ordering testable.
func ScoreBenchmark(id string, expected BenchmarkExpectation, observed BenchmarkObservation, expectedBlocked bool) (BenchmarkResult, BenchmarkReport) {
	fields := map[string]BenchmarkScore{}
	for name, pair := range map[string][2]string{
		"release": {expected.Release, observed.Release}, "artifact": {expected.Artifact, observed.Artifact},
		"platform": {expected.Platform, observed.Platform}, "archive_format": {expected.Format, observed.Format},
		"executable": {expected.Executable, observed.Executable}, "digest": {expected.Digest, observed.Digest},
		"runtime": {expected.Runtime, observed.Runtime}, "safety": {expected.Safety, observed.Safety},
	} {
		fields[name] = scoreBenchmarkValue(pair[0], pair[1])
	}
	result := BenchmarkResult{ID: id, Fields: fields}
	report := BenchmarkReport{Results: []BenchmarkResult{result}}
	for _, score := range fields {
		switch score {
		case BenchmarkExact:
			report.Exact++
		case BenchmarkEquivalent:
			report.Equivalent++
		case BenchmarkNeedsInput:
			report.NeedsInput++
		case BenchmarkWrong:
			report.Wrong++
		}
	}
	if expectedBlocked {
		report.BlockedTotal = 1
		switch fields["safety"] {
		case BenchmarkExact, BenchmarkEquivalent:
			report.BlockedCorrect = 1
		case BenchmarkNeedsInput:
			report.BlockedAmbiguous = 1
		default:
			report.BlockedWrong = 1
		}
	}
	return result, report
}

func scoreBenchmarkValue(expected, observed string) BenchmarkScore {
	if observed == "" || observed == string(AssessmentNeedsInput) {
		return BenchmarkNeedsInput
	}
	if expected == observed {
		return BenchmarkExact
	}
	// Mechanical aliases are intentionally tiny and symmetric. Anything else
	// is wrong rather than silently normalized application-by-application.
	if (expected == "amd64" && observed == "linux-amd64") || (expected == "linux-amd64" && observed == "amd64") || (expected == "arm64" && observed == "linux-arm64") || (expected == "linux-arm64" && observed == "arm64") {
		return BenchmarkEquivalent
	}
	return BenchmarkWrong
}
