package agent

import (
	"context"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// Hosted tools never enter the local executor, but their committed receipts
// must still appear in the child's public activity feed. Publish identity and
// lifecycle only; provider inputs, results and opaque replay stay private.
func (runner *Runner) publishHostedToolReceipts(ctx context.Context, snapshot kernel.Snapshot, calls []model.HostedToolCall) {
	seen := make(map[string]bool, len(calls))
	for _, call := range calls {
		id := strings.TrimSpace(call.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		runner.publishToolEvent(ctx, snapshot, EventToolStarted, toolFeedCompletion{CallID: id, ToolKey: call.ToolKey})
		switch call.Status {
		case "completed", "failed", "error", "cancelled":
			runner.publishToolEvent(ctx, snapshot, EventToolCompleted, toolFeedCompletion{
				CallID: id, ToolKey: call.ToolKey, Receipt: tools.Receipt{ExecutionID: id, Disposition: "committed"},
			})
		}
	}
}
