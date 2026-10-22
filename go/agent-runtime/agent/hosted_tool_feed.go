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
	for _, call := range model.CollapseHostedToolCalls(calls) {
		id := strings.TrimSpace(call.ID)
		if id == "" {
			continue
		}
		runner.publishToolEvent(ctx, snapshot, EventToolStarted, toolFeedCompletion{CallID: id, ToolKey: call.ToolKey})
		if model.HostedToolTerminalStatus(call.Status) {
			runner.publishToolEvent(ctx, snapshot, EventToolCompleted, toolFeedCompletion{
				CallID: id, ToolKey: call.ToolKey, Receipt: tools.Receipt{ExecutionID: id, Disposition: "committed"},
			})
		}
	}
}
