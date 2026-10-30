package agent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// pendingToolApproval binds a durable approval to one precise Tool call and
// the checkpoint presented to the approver, never an entire batch.
type pendingToolApproval struct {
	CheckpointID    string `json:"checkpointID"`
	CallID          string `json:"callID"`
	ToolKey         string `json:"toolKey"`
	ArgumentsDigest string `json:"argumentsDigest"`
	PayloadDigest   string `json:"payloadDigest"`
}

func newPendingToolApproval(checkpoint *kernel.Checkpoint, call tools.Call) (*pendingToolApproval, error) {
	if checkpoint == nil || strings.TrimSpace(checkpoint.ID) == "" ||
		strings.TrimSpace(call.ID) == "" || strings.TrimSpace(call.ToolKey) == "" ||
		!json.Valid(checkpoint.Payload) || !json.Valid(call.Arguments) {
		return nil, ErrApprovalRequired
	}
	digest, err := toolApprovalArgumentsDigest(call.Arguments)
	if err != nil {
		return nil, err
	}
	return &pendingToolApproval{
		CheckpointID: checkpoint.ID, CallID: call.ID, ToolKey: call.ToolKey,
		ArgumentsDigest: digest, PayloadDigest: approvalHash(checkpoint.Payload),
	}, nil
}

func approvalHash(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

func toolApprovalArgumentsDigest(arguments json.RawMessage) (string, error) {
	// json.Marshal normalizes RawMessage formatting without changing its value.
	canonical, err := json.Marshal(arguments)
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrApprovalRequired, err)
	}
	return approvalHash(canonical), nil
}

func (binding *pendingToolApproval) matches(checkpoint *kernel.Checkpoint, call tools.Call) bool {
	if binding == nil || checkpoint == nil || checkpoint.ID != binding.CheckpointID ||
		binding.CallID != call.ID || binding.ToolKey != call.ToolKey ||
		approvalHash(checkpoint.Payload) != binding.PayloadDigest {
		return false
	}
	digest, err := toolApprovalArgumentsDigest(call.Arguments)
	return err == nil && digest == binding.ArgumentsDigest
}

// approvalForPendingCall requires the exact committed Tool/call/checkpoint
// binding. A checkpoint without this persisted evidence cannot authorize a
// Tool after restart, even when a legacy payload happens to match.
func approvalForPendingCall(snapshot kernel.Snapshot, state runState) (*pendingToolApproval, bool) {
	call, ok := nextPendingCall(state)
	if !ok || snapshot.Checkpoint == nil || state.PendingApproval == nil {
		return nil, false
	}
	return state.PendingApproval, state.PendingApproval.matches(snapshot.Checkpoint, call)
}

func approvalDecision(checkpoint *kernel.Checkpoint) (plugin.ApprovalDecision, bool) {
	if checkpoint == nil || checkpoint.Status != kernel.CheckpointResolved || !json.Valid(checkpoint.Response) {
		return "", false
	}
	var response plugin.ApprovalResponse
	if err := json.Unmarshal(checkpoint.Response, &response); err != nil ||
		(response.Decision != plugin.ApprovalApprove && response.Decision != plugin.ApprovalReject) {
		return "", false
	}
	return response.Decision, true
}

func approvedPendingCall(snapshot kernel.Snapshot, state runState) bool {
	if _, ok := approvalForPendingCall(snapshot, state); !ok {
		return false
	}
	decision, ok := approvalDecision(snapshot.Checkpoint)
	return ok && decision == plugin.ApprovalApprove
}

func validResolvedApproval(before, resolved *kernel.Checkpoint, decision plugin.ApprovalDecision) bool {
	if before == nil || resolved == nil || before.ID != resolved.ID ||
		before.Kind != resolved.Kind || before.Status != kernel.CheckpointPending ||
		approvalHash(before.Payload) != approvalHash(resolved.Payload) {
		return false
	}
	got, ok := approvalDecision(resolved)
	return ok && got == decision
}
