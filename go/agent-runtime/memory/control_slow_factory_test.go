package memory_test

import (
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/conformance"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

// Store setup (for example PostgreSQL migrations) must not consume the Run's
// deliberately short deadline before Kernel.Create even receives the request.
func TestKernelControlDeadlineAfterSlowStoreSetup(t *testing.T) {
	conformance.RunKernelControlSuite(t, func(t *testing.T) kernel.Store {
		if strings.Contains(t.Name(), "deadline-checks-store-time") || strings.Contains(t.Name(), "Store-deadline-gates-approval-and-allowance/") {
			time.Sleep(900 * time.Millisecond)
		}
		return memory.NewStore()
	})
}
