package app

import (
	"context"
	"path/filepath"

	"github.com/drobilica/tarlink/internal/research"
)

func (core *Core) candidateLedger() (research.CandidateLedger, error) {
	return research.LoadLedger(filepath.Join("registry-research", "candidates.yaml"))
}

func (core *Core) CandidateChanges(ctx context.Context) (research.CandidateChanges, error) {
	l, err := core.candidateLedger()
	if err != nil {
		return research.CandidateChanges{}, err
	}
	c := &research.Client{CacheRoot: filepath.Join(core.layout.Cache, "registry-research")}
	if core.syncer != nil && core.syncer.Client != nil {
		c.HTTP = core.syncer.Client.HTTP
	}
	return research.DetectChanges(ctx, c, l), nil
}
func (core *Core) CandidateLedger() (research.CandidateLedger, error) { return core.candidateLedger() }
func (core *Core) Blockers(capability string) ([]research.BlockerSummary, error) {
	l, e := core.candidateLedger()
	if e != nil {
		return nil, e
	}
	return research.SummarizeBlockers(l, capability)
}
func (core *Core) CapabilityPreflight(capability string) ([]research.CapabilityResult, error) {
	l, e := core.candidateLedger()
	if e != nil {
		return nil, e
	}
	return research.AnalyzeCapability(l, capability)
}
