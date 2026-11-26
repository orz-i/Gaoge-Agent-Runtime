package harness_test

import (
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

func TestNativeAuthorityCutover(t *testing.T) {
	conformance.NativeAuthorityCutover(t, func(t *testing.T) conformance.NativeAuthorityFixture {
		return conformance.NativeAuthorityFixture{Kernel: memory.NewStore(), Host: harness.NewMemoryStore(), Relations: memory.NewRunRelationStore()}
	})
}
