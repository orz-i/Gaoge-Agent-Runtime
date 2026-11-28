package conformance

import (
	"context"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

// RunEA1RootAgentResumeSettlementSuite covers AR-R P3/P4 through actual Resume
// of a PrepareRun-created Root. Root has no ancestor. Memory retains state;
// SQLite reopens and real PostgreSQL uses an independent connection pool.
func RunEA1RootAgentResumeSettlementSuite(t *testing.T, factory func(*testing.T) (kernel.Store, func() kernel.Store)) {
	for _, phase := range []string{"P3", "P4"} {
		for _, tool := range []bool{false, true} {
			kind := map[bool]string{false: "model-yield", true: "terminal-tool"}[tool]
			t.Run(phase+"/"+kind, func(t *testing.T) { ea1RootSettlement(t, factory, phase, tool, false, "resume", false) })
		}
	}
	t.Run("P3/committed-model-receipt-unknown-host", func(t *testing.T) { ea1RootSettlement(t, factory, "P3", false, true, "resume", false) })
}

// Keep the Start middleware contract unchanged; this adapter admits only Resume.
type ea1ResumeSettlementMiddleware struct{ physical *ea1StartSettlementPhysical }

func (ea1ResumeSettlementMiddleware) Name() string { return "ea1.original-resume-callback" }
func (m ea1ResumeSettlementMiddleware) Run(ctx context.Context, inv plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	p := m.physical
	p.mu.Lock()
	p.hostCalls++
	p.mu.Unlock()
	if inv.Operation != plugin.RunResume || inv.ParentRunID != "" {
		return kernel.Snapshot{}, errors.New("not original Root Resume callback")
	}
	result, err := next(ctx)
	if p.hostUnknown {
		p.mu.Lock()
		p.hostSends++
		p.mu.Unlock()
		return result, errors.Join(err, p.hostFault)
	}
	return result, err
}
