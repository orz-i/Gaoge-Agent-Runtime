package conformance_test

import (
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness/conformance"
)

func TestMemoryCheckpointOpaqueEvidence(t *testing.T) {
	conformance.CheckpointOpaqueEvidence(t, harness.NewMemoryStore(), nil)
}
