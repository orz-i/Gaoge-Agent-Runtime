package conformance

import (
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
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
}
