package research

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

var candidateIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

type CandidateLedger struct {
	Candidates []Candidate `yaml:"candidates" json:"candidates"`
}
type Candidate struct {
	ID             string          `yaml:"id" json:"id"`
	Upstream       string          `yaml:"upstream" json:"upstream"`
	LastChecked    ReleaseIdentity `yaml:"last_checked" json:"last_checked"`
	Status         string          `yaml:"status" json:"status"`
	Blockers       []string        `yaml:"blockers,omitempty" json:"blockers,omitempty"`
	ReconsiderWhen []string        `yaml:"reconsider_when,omitempty" json:"reconsider_when,omitempty"`
	Notes          string          `yaml:"notes,omitempty" json:"notes,omitempty"`
}
type ReleaseIdentity struct {
	ReleaseTag string `yaml:"release_tag" json:"release_tag"`
	ReleaseID  int64  `yaml:"release_id" json:"release_id"`
}

type CandidateDecision struct {
	ID       string           `json:"id"`
	Status   string           `json:"status"`
	Decision string           `json:"decision"`
	Old      ReleaseIdentity  `json:"old_release"`
	Current  *ReleaseIdentity `json:"current_release,omitempty"`
	Reason   string           `json:"reason,omitempty"`
	Error    string           `json:"error,omitempty"`
}
type CandidateChanges struct {
	Summary map[string]int      `json:"summary"`
	Results []CandidateDecision `json:"results"`
}
type CapabilityResult struct {
	ID            string   `json:"id"`
	Removed       []string `json:"removed"`
	Remaining     []string `json:"remaining"`
	FullyUnlocked bool     `json:"fully_unlocked"`
}
type BlockerSummary struct {
	Blocker string `json:"blocker"`
	Count   int    `json:"count"`
}
type Capability struct {
	ID       string
	Blockers map[string]bool
}

var capabilities = map[string]Capability{
	"appimage-metadata": {ID: "appimage-metadata", Blockers: map[string]bool{"APPIMAGE_METADATA_UNSUPPORTED": true}},
}
var knownBlockers = map[string]bool{
	"UNSUPPORTED_ARTIFACT": true, "UNSUPPORTED_ARCH": true,
	"NO_EXECUTABLE": true, "APPIMAGE_METADATA_UNSUPPORTED": true,
	"NO_LINUX_ARTIFACT": true, "MUTABLE_ARTIFACT": true, "SYSTEM_INTEGRATION_REQUIRED": true,
	"SETUP_SCRIPT_REQUIRED": true, "WINDOWS_ONLY": true, "SOURCE_ONLY": true,
}
var reconsiderPrefixes = []string{"new-upstream-release", "manual", "capability:"}

func Capabilities() []Capability {
	out := make([]Capability, 0, len(capabilities))
	for _, c := range capabilities {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
func ValidateLedger(l CandidateLedger) error {
	ids, repos := map[string]bool{}, map[Repository]bool{}
	for _, c := range l.Candidates {
		if !candidateIDPattern.MatchString(c.ID) {
			return fmt.Errorf("invalid candidate id %q", c.ID)
		}
		r, err := ParseRepository(c.Upstream)
		if err != nil {
			return fmt.Errorf("candidate %s: %w", c.ID, err)
		}
		if ids[c.ID] {
			return fmt.Errorf("duplicate candidate id %q", c.ID)
		}
		ids[c.ID] = true
		if repos[r] {
			return fmt.Errorf("duplicate upstream repository %q", r)
		}
		repos[r] = true
		if c.Status != "blocked" && c.Status != "deferred" && c.Status != "rejected" && c.Status != "ready" {
			return fmt.Errorf("candidate %s: invalid status %q", c.ID, c.Status)
		}
		if c.LastChecked.ReleaseID <= 0 || strings.TrimSpace(c.LastChecked.ReleaseTag) == "" {
			return fmt.Errorf("candidate %s: missing last-checked release identity", c.ID)
		}
		for _, b := range c.Blockers {
			if !knownBlockers[b] {
				return fmt.Errorf("candidate %s: unknown blocker %q", c.ID, b)
			}
		}
		for _, cond := range c.ReconsiderWhen {
			good := false
			for _, p := range reconsiderPrefixes {
				if cond == p || strings.HasPrefix(cond, p) {
					good = true
					break
				}
			}
			if !good {
				return fmt.Errorf("candidate %s: invalid reconsideration condition %q", c.ID, cond)
			}
			if strings.HasPrefix(cond, "capability:") {
				if _, ok := capabilities[strings.TrimPrefix(cond, "capability:")]; !ok {
					return fmt.Errorf("candidate %s: unknown capability %q", c.ID, cond)
				}
			}
		}
	}
	return nil
}
func LoadLedger(path string) (CandidateLedger, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return CandidateLedger{}, err
	}
	var l CandidateLedger
	dec := yaml.NewDecoder(strings.NewReader(string(b)))
	dec.KnownFields(true)
	if err = dec.Decode(&l); err != nil {
		return CandidateLedger{}, fmt.Errorf("decode candidate ledger: %w", err)
	}
	if err = ValidateLedger(l); err != nil {
		return CandidateLedger{}, err
	}
	return l, nil
}

func AnalyzeCapability(l CandidateLedger, id string) ([]CapabilityResult, error) {
	cap, ok := capabilities[id]
	if !ok {
		return nil, fmt.Errorf("unknown capability %q", id)
	}
	out := []CapabilityResult{}
	for _, c := range l.Candidates {
		if c.Status == "rejected" || c.Status == "deferred" {
			continue
		}
		var rm, remain []string
		for _, b := range c.Blockers {
			if cap.Blockers[b] {
				rm = append(rm, b)
			} else {
				remain = append(remain, b)
			}
		}
		if len(rm) > 0 {
			out = append(out, CapabilityResult{ID: c.ID, Removed: rm, Remaining: remain, FullyUnlocked: len(remain) == 0})
		}
	}
	return out, nil
}
func SummarizeBlockers(l CandidateLedger, filter string) ([]BlockerSummary, error) {
	if filter != "" {
		if _, ok := capabilities[filter]; !ok {
			return nil, fmt.Errorf("unknown capability %q", filter)
		}
	}
	counts := map[string]int{}
	for _, c := range l.Candidates {
		if c.Status == "rejected" {
			continue
		}
		for _, b := range c.Blockers {
			if filter == "" || capabilities[filter].Blockers[b] {
				counts[b]++
			}
		}
	}
	out := make([]BlockerSummary, 0, len(counts))
	for b, n := range counts {
		out = append(out, BlockerSummary{b, n})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Blocker < out[j].Blocker })
	return out, nil
}

func DetectChanges(ctx context.Context, client *Client, l CandidateLedger) CandidateChanges {
	out := CandidateChanges{Summary: map[string]int{"RECHECK": 0, "UNCHANGED": 0, "ERROR": 0}}
	for _, c := range l.Candidates {
		d := CandidateDecision{ID: c.ID, Status: c.Status, Old: c.LastChecked, Decision: "UNCHANGED"}
		watchRelease := c.Status != "rejected"
		if watchRelease {
			watchRelease = false
			for _, condition := range c.ReconsiderWhen {
				if condition == "new-upstream-release" {
					watchRelease = true
					break
				}
			}
		}
		if !watchRelease {
			out.Results = append(out.Results, d)
			out.Summary["UNCHANGED"]++
			continue
		}
		releases, err := client.Discover(ctx, c.Upstream)
		if err != nil {
			d.Decision = "ERROR"
			d.Reason = "DISCOVERY_ERROR"
			d.Error = err.Error()
			out.Summary["ERROR"]++
		} else if len(releases) == 0 {
			d.Decision = "ERROR"
			d.Error = "no releases found"
			out.Summary["ERROR"]++
		} else {
			r := releases[0]
			cur := ReleaseIdentity{ReleaseTag: r.Tag, ReleaseID: r.ID}
			d.Current = &cur
			if r.ID != c.LastChecked.ReleaseID {
				d.Decision = "RECHECK"
				if r.Tag == c.LastChecked.ReleaseTag {
					d.Reason = "RECREATED_RELEASE"
				} else {
					d.Reason = "NEW_RELEASE"
				}
				out.Summary["RECHECK"]++
			} else {
				out.Summary["UNCHANGED"]++
			}
		}
		out.Results = append(out.Results, d)
	}
	return out
}
