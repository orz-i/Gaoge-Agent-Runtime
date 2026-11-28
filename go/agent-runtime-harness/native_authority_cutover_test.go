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

func TestNativeExternalCancellationMemory(t *testing.T) {
	conformance.NativeExternalCancellation(t, func(t *testing.T) conformance.NativeAuthorityFixture {
		return conformance.NativeAuthorityFixture{Kernel: memory.NewStore(), Host: harness.NewMemoryStore(), Relations: memory.NewRunRelationStore()}
	})
}

func TestNativeExternalCancellationRecoveryMemory(t *testing.T) {
	conformance.NativeExternalCancellationRecovery(t, func(t *testing.T) conformance.NativeAuthorityFixture {
		return conformance.NativeAuthorityFixture{Kernel: memory.NewStore(), Host: harness.NewMemoryStore(), Relations: memory.NewRunRelationStore()}
	})
}

func TestNativeExternalCancellationConcurrentMemory(t *testing.T) {
	conformance.NativeExternalCancellationConcurrent(t, func(t *testing.T) conformance.NativeAuthorityFixture {
		return conformance.NativeAuthorityFixture{Kernel: memory.NewStore(), Host: harness.NewMemoryStore(), Relations: memory.NewRunRelationStore()}
	})
}
