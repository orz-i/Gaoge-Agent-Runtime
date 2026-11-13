package memory_test

import (
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"testing"
	"time"
)

func TestManagedMemoryStoreConformance(t *testing.T) {
	conformance.RunManagedRecoveryStoreSuite(t, func(t *testing.T) conformance.ManagedRecoveryStoreFixture {
		clock := &ownedPhysicalClock{now: time.Now().UTC()}
		store := memory.NewStoreWithExecutionClock(clock)
		return conformance.ManagedRecoveryStoreFixture{Store: store, Peer: store, AdvanceTo: func(target time.Time) {
			if clock.now.Before(target) {
				clock.now = target
			}
		}, Reopen: func() kernel.Store { return store }}
	})
}
