package agent

import (
	"bytes"
	"reflect"
	"slices"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// VerifyFrozenStartRequest is a read-only Host replay/child-origin verifier
// against the ORIGINAL Agent runner's effective admission policy. Equal
// Run IDs and apparent Goal/Model/Tool labels cannot authorize reuse of an
// Agent that persisted wider execution limits or another initial transcript.
//
// This method deliberately does not call the Catalog or any middleware:
// pending and interrupted Runs must verify their frozen initial authority
// without redispatching a Tool or reevaluating a dynamic Host policy.
func (runner *Runner) VerifyFrozenStartRequest(snapshot kernel.Snapshot, request StartRequest) bool {
	if runner == nil || runner.runtime == nil ||
		snapshot.Run.ID == "" || request.ID == "" || snapshot.Run.ID != request.ID ||
		snapshot.Run.Kind != RunKind || snapshot.Run.Actor != request.Actor ||
		snapshot.Run.Thread != request.Thread ||
		snapshot.Run.RequestID != strings.TrimSpace(request.RequestID) ||
		snapshot.Run.Goal != strings.TrimSpace(request.Goal) {
		return false
	}
	if len(request.ModelOptions) != 0 &&
		jsoncontract.ValidateUniqueJSONObject(request.ModelOptions) != nil {
		// Two textual representations must not be promoted to equivalent
		// original policy when their duplicate keys select different values.
		return false
	}
	state, err := decodeState(snapshot.State)
	if err != nil {
		return false
	}
	limits, err := resolveRunLimits(runner.limits, request.Limits)
	if err != nil || state.Budget.Limits != limits ||
		state.Model != strings.TrimSpace(request.Model) ||
		!bytes.Equal(state.ModelOptions, request.ModelOptions) ||
		!slices.Equal(state.ToolKeys, normalizedToolKeys(request.ToolKeys)) ||
		len(state.RequiredToolKeys) != 0 || len(state.HostedToolGrants) != 0 ||
		state.OutputContract != nil {
		return false
	}
	initial := []model.Message{{
		Role: model.RoleUser, Content: model.TextParts(strings.TrimSpace(request.Goal)),
	}}
	if instructions := strings.TrimSpace(request.Instructions); instructions != "" {
		initial = append([]model.Message{{
			Role: model.RoleSystem, Content: model.TextParts(instructions),
		}}, initial...)
	}
	return len(state.Messages) >= len(initial) &&
		reflect.DeepEqual(state.Messages[:len(initial)], initial)
}
