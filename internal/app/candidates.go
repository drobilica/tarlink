package app

import (
	"context"
	"path/filepath"

	"github.com/drobilica/tarlink/internal/research"
)

func candidateLedger() (research.CandidateLedger, error) {
	return research.LoadLedger(filepath.Join("registry-research", "candidates.yaml"))
}

func (m *Maintainer) CandidateChanges(ctx context.Context) (research.CandidateChanges, error) {
	l, err := candidateLedger()
	if err != nil {
		return research.CandidateChanges{}, err
	}
	c := &research.Client{CacheRoot: filepath.Join(m.layout.Cache, "registry-research")}
	if m.client != nil {
		c.HTTP = m.client.HTTP
	}
	return research.DetectChanges(ctx, c, l), nil
}
func (m *Maintainer) CandidateLedger() (research.CandidateLedger, error) { return candidateLedger() }
func (m *Maintainer) Blockers(capability string) ([]research.BlockerSummary, error) {
	l, e := candidateLedger()
	if e != nil {
		return nil, e
	}
	return research.SummarizeBlockers(l, capability)
}
func (m *Maintainer) CapabilityPreflight(capability string) ([]research.CapabilityResult, error) {
	l, e := candidateLedger()
	if e != nil {
		return nil, e
	}
	return research.AnalyzeCapability(l, capability)
}
