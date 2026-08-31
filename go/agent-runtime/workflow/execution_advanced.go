package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

type effectPlan struct {
	Call         EffectCall
	OutputKey    string
	MapIndex     int
	Mapped       bool
	Compensation bool
	Input        json.RawMessage
}

type effectOutcome struct {
	EffectIndex int
	Result      EffectResult
	Err         error
}

func nodeHasEffects(node Node) bool {
	switch node.Type {
	case NodeEffect, NodeAgentTask, NodeApplicationEffect, NodeMediaEffect,
		NodeParallel, NodeMap, NodeSubworkflow, NodeCompensation:
		return true
	case NodeWait, NodeIf, NodeReturn:
		return false
	default:
		return false
	}
}

func (runner *Runner) prepareNodeEffects(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	node Node,
) (kernel.Snapshot, error) {
	plans, err := effectPlansForNode(state, node)
	if err != nil || len(plans) == 0 && node.Type != NodeMap {
		return runner.fail(ctx, snapshot, state, "workflow.effect_plan", errors.Join(ErrInvalidExecution, err))
	}
	if state.Budget.Effects+len(plans) > state.Definition.Limits.MaxEffects {
		return runner.fail(ctx, snapshot, state, "workflow.effect_budget", ErrBudgetExceeded)
	}
	var reserved int64
	for _, plan := range plans {
		if plan.Call.MaxCostUnits < 0 || reserved > state.Definition.Policy.MaxCostUnits-plan.Call.MaxCostUnits {
			return runner.fail(ctx, snapshot, state, "workflow.cost_budget", ErrBudgetExceeded)
		}
		reserved += plan.Call.MaxCostUnits
	}
	if state.Budget.CostUnitsUsed+state.Budget.CostUnitsReserved > state.Definition.Policy.MaxCostUnits-reserved {
		return runner.fail(ctx, snapshot, state, "workflow.cost_budget", ErrBudgetExceeded)
	}

	activationID, err := runner.runtime.NewID("activation")
	if err != nil {
		return kernel.Snapshot{}, err
	}
	activation := Activation{
		ID: activationID, NodeID: node.ID, NodeIndex: state.CurrentNode,
		Status: ActivationRunning, Attempt: 1, EffectIDs: make([]string, 0, len(plans)),
	}
	for _, plan := range plans {
		effectID, idErr := runner.runtime.NewID("effect")
		if idErr != nil {
			return kernel.Snapshot{}, idErr
		}
		activation.EffectIDs = append(activation.EffectIDs, effectID)
		nestedDepth := state.NestedDepth
		if plan.Call.Class == EffectClassSubworkflow {
			nestedDepth++
		}
		state.Effects = append(state.Effects, Effect{
			ID: effectID, ActivationID: activationID, NodeID: node.ID,
			Class: plan.Call.Class, Kind: plan.Call.Kind, Revision: plan.Call.Revision,
			Definition: cloneDefinitionReference(plan.Call.Definition), OutputKey: plan.OutputKey,
			MapIndex: plan.MapIndex, Mapped: plan.Mapped, Compensation: plan.Compensation,
			Input: cloneJSON(plan.Input), MaxCostUnits: plan.Call.MaxCostUnits,
			NestedDepth: nestedDepth, Attempt: 1, Retry: plan.Call.Retry, Status: EffectPending,
		})
	}
	if len(activation.EffectIDs) == 1 {
		activation.EffectID = activation.EffectIDs[0]
	}
	state.Activations = append(state.Activations, activation)
	state.Budget.NodeActivations++
	state.Budget.Effects += len(plans)
	state.Budget.CostUnitsReserved += reserved
	encoded, err := encodeExecutionState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{
			Type: "workflow.effect.intents_created", Message: strings.Join(activation.EffectIDs, ","),
		}},
	})
}

func effectPlansForNode(state executionState, node Node) ([]effectPlan, error) {
	switch node.Type {
	case NodeEffect:
		input, err := resolveLegacyEffectInput(state, *node.Effect)
		return []effectPlan{{Call: EffectCall{
			Class: EffectClassGeneric, Kind: node.Effect.Kind, MaxCostUnits: node.Effect.MaxCostUnits,
			Retry: node.Effect.Retry,
		}, Input: input}}, err
	case NodeAgentTask:
		return plansFromCalls(state, []EffectCall{{
			Class: EffectClassAgent, Kind: node.AgentTask.AgentKey, Revision: node.AgentTask.Revision,
			Input: node.AgentTask.Input, MaxCostUnits: node.AgentTask.MaxCostUnits,
			Retry: node.AgentTask.Retry,
		}}, nil, false)
	case NodeApplicationEffect:
		return plansFromCalls(state, []EffectCall{{
			Class: EffectClassApplication, Kind: node.ApplicationEffect.CapabilityKey,
			Revision: node.ApplicationEffect.Revision, Input: node.ApplicationEffect.Input,
			MaxCostUnits: node.ApplicationEffect.MaxCostUnits,
			Retry:        node.ApplicationEffect.Retry,
		}}, nil, false)
	case NodeMediaEffect:
		return plansFromCalls(state, []EffectCall{{
			Class: EffectClassMedia, Kind: node.MediaEffect.CapabilityKey, Revision: node.MediaEffect.Revision,
			Input: node.MediaEffect.Input, MaxCostUnits: node.MediaEffect.MaxCostUnits,
			Retry: node.MediaEffect.Retry,
		}}, nil, false)
	case NodeParallel:
		calls := make([]EffectCall, len(node.Parallel.Branches))
		keys := make([]string, len(node.Parallel.Branches))
		for index, branch := range node.Parallel.Branches {
			calls[index] = branch.Call
			keys[index] = branch.ID
		}
		return plansFromCalls(state, calls, keys, false)
	case NodeMap:
		items, err := resolveValueSource(state, node.Map.Items, nil, 0)
		if err != nil {
			return nil, err
		}
		var values []json.RawMessage
		if err = json.Unmarshal(items, &values); err != nil || len(values) > state.Definition.Limits.MaxFanOut {
			return nil, ErrInvalidExecution
		}
		plans := make([]effectPlan, len(values))
		for index, item := range values {
			input, inputErr := resolveValueSource(state, node.Map.Call.Input, item, index)
			if inputErr != nil {
				return nil, inputErr
			}
			plans[index] = effectPlan{Call: node.Map.Call, MapIndex: index, Mapped: true, Input: input}
		}
		return plans, nil
	case NodeSubworkflow:
		if state.NestedDepth >= state.Definition.Limits.MaxNestedDepth {
			return nil, ErrBudgetExceeded
		}
		input, err := resolveValueSource(state, node.Subworkflow.Input, nil, 0)
		return []effectPlan{{Call: EffectCall{
			Class: EffectClassSubworkflow, Kind: "workflow.run", Definition: &node.Subworkflow.Definition,
			Input: node.Subworkflow.Input, MaxCostUnits: node.Subworkflow.MaxCostUnits,
			Retry: node.Subworkflow.Retry,
		}, Input: input}}, err
	case NodeCompensation:
		input, err := resolveValueSource(state, node.Compensation.Do.Input, nil, 0)
		return []effectPlan{{Call: node.Compensation.Do, Input: input}}, err
	case NodeWait, NodeIf, NodeReturn:
		return nil, ErrInvalidExecution
	default:
		return nil, ErrInvalidExecution
	}
}

func plansFromCalls(
	state executionState,
	calls []EffectCall,
	keys []string,
	compensation bool,
) ([]effectPlan, error) {
	plans := make([]effectPlan, len(calls))
	for index, call := range calls {
		input, err := resolveValueSource(state, call.Input, nil, 0)
		if err != nil {
			return nil, err
		}
		key := ""
		if index < len(keys) {
			key = keys[index]
		}
		plans[index] = effectPlan{Call: call, OutputKey: key, Input: input, Compensation: compensation}
	}
	return plans, nil
}

func resolveLegacyEffectInput(state executionState, node EffectNode) (json.RawMessage, error) {
	if node.Source != nil {
		return resolveValueSource(state, *node.Source, nil, 0)
	}
	if node.FromInput {
		return cloneJSON(state.Input), nil
	}
	if !json.Valid(node.Input) {
		return nil, ErrInvalidExecution
	}
	return cloneJSON(node.Input), nil
}

func resolveValueSource(
	state executionState,
	source ValueSource,
	mapItem json.RawMessage,
	mapIndex int,
) (json.RawMessage, error) {
	var value json.RawMessage
	switch source.Kind {
	case ValueLiteral:
		value = cloneJSON(source.Value)
	case ValueWorkflowInput:
		value = cloneJSON(state.Input)
	case ValueNodeOutput:
		activation := latestCompletedActivation(state, source.NodeID)
		if activation == nil {
			return nil, ErrInvalidExecution
		}
		value = cloneJSON(activation.Output)
	case ValueWaitResponse:
		for index := len(state.Waits) - 1; index >= 0; index-- {
			if state.Waits[index].NodeID == source.NodeID && state.Waits[index].Status == WaitResolved {
				value = cloneJSON(state.Waits[index].Response)
				break
			}
		}
	case ValueMapItem:
		value = cloneJSON(mapItem)
	case ValueMapIndex:
		value = json.RawMessage(strconv.Itoa(mapIndex))
	default:
		return nil, ErrInvalidExecution
	}
	if !json.Valid(value) {
		return nil, ErrInvalidExecution
	}
	if source.Pointer == "" {
		return value, nil
	}
	return resolveJSONPointer(value, source.Pointer)
}

func latestCompletedActivation(state executionState, nodeID string) *Activation {
	for index := len(state.Activations) - 1; index >= 0; index-- {
		activation := &state.Activations[index]
		if activation.NodeID == nodeID && activation.Status == ActivationCompleted && json.Valid(activation.Output) {
			return activation
		}
	}
	return nil
}

func resolveJSONPointer(value json.RawMessage, pointer string) (json.RawMessage, error) {
	if pointer == "" {
		return cloneJSON(value), nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, ErrInvalidExecution
	}
	decoder := json.NewDecoder(strings.NewReader(string(value)))
	decoder.UseNumber()
	var current any
	if err := decoder.Decode(&current); err != nil {
		return nil, errors.Join(ErrInvalidExecution, err)
	}
	for _, encodedPart := range strings.Split(pointer[1:], "/") {
		part := strings.ReplaceAll(strings.ReplaceAll(encodedPart, "~1", "/"), "~0", "~")
		switch typed := current.(type) {
		case map[string]any:
			var exists bool
			current, exists = typed[part]
			if !exists {
				return nil, ErrInvalidExecution
			}
		case []any:
			index, err := strconv.Atoi(part)
			if err != nil || index < 0 || index >= len(typed) {
				return nil, ErrInvalidExecution
			}
			current = typed[index]
		default:
			return nil, ErrInvalidExecution
		}
	}
	resolved, err := json.Marshal(current)
	if err != nil {
		return nil, errors.Join(ErrInvalidExecution, err)
	}
	return resolved, nil
}

func nextNodeIndex(state executionState, node Node) (int, error) {
	if node.Next == "" {
		return state.CurrentNode + 1, nil
	}
	return nodeIndexByID(state.Definition, node.Next)
}

func nodeIndexByID(definition Definition, nodeID string) (int, error) {
	for index, node := range definition.Nodes {
		if node.ID == nodeID {
			return index, nil
		}
	}
	return 0, ErrInvalidExecution
}

func cloneDefinitionReference(reference *DefinitionReference) *DefinitionReference {
	if reference == nil {
		return nil
	}
	clone := *reference
	return &clone
}

func effectConcurrency(node Node) int {
	switch node.Type {
	case NodeParallel:
		return node.Parallel.MaxConcurrency
	case NodeMap:
		return node.Map.MaxConcurrency
	case NodeEffect, NodeAgentTask, NodeApplicationEffect, NodeMediaEffect,
		NodeWait, NodeIf, NodeSubworkflow, NodeCompensation, NodeReturn:
		return 1
	default:
		return 1
	}
}

func buildEffectRequest(run kernel.Run, definition Definition, effect Effect) EffectRequest {
	return EffectRequest{
		RunID: run.ID, Actor: run.Actor, Thread: run.Thread,
		DefinitionID: definition.ID, DefinitionHash: definition.Hash,
		EffectID: effect.ID, NodeID: effect.NodeID, Class: effect.Class, Kind: effect.Kind,
		Revision: effect.Revision, Definition: cloneDefinitionReference(effect.Definition),
		OutputKey: effect.OutputKey, MapIndex: effect.MapIndex, Compensation: effect.Compensation,
		Input: cloneJSON(effect.Input), MaxCostUnits: effect.MaxCostUnits,
		NestedDepth: effect.NestedDepth,
		Attempt:     effect.Attempt, MaxAttempts: effect.Retry.MaxAttempts,
		Policy: cloneDefinitionPolicy(definition.Policy),
	}
}

func (runner *Runner) executeEffectBatch(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	effectIndices []int,
	maxConcurrency int,
) []effectOutcome {
	if maxConcurrency <= 0 || maxConcurrency > len(effectIndices) {
		maxConcurrency = len(effectIndices)
	}
	outcomes := make([]effectOutcome, len(effectIndices))
	semaphore := make(chan struct{}, maxConcurrency)
	var group sync.WaitGroup
	for position, effectIndex := range effectIndices {
		position, effectIndex := position, effectIndex
		group.Add(1)
		go func() {
			defer group.Done()
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			effect := state.Effects[effectIndex]
			request := buildEffectRequest(snapshot.Run, state.Definition, effect)
			var result EffectResult
			var err error
			if effect.Class == EffectClassSubworkflow && runner.registry != nil {
				result, err = runner.executeObservedEffectFunc(ctx, request, runner.executeSubworkflowEffect)
			} else {
				result, err = runner.executeObservedEffect(ctx, request)
			}
			outcomes[position] = effectOutcome{EffectIndex: effectIndex, Result: result, Err: err}
		}()
	}
	group.Wait()
	return outcomes
}

func pendingEffectIndices(state executionState, activation Activation) ([]int, error) {
	indices := make([]int, 0, len(activation.EffectIDs))
	for _, effectID := range activation.EffectIDs {
		index := effectIndexByID(state, effectID)
		if index < 0 {
			return nil, ErrInvalidExecution
		}
		if state.Effects[index].Status == EffectPending {
			indices = append(indices, index)
		}
	}
	return indices, nil
}

func aggregateEffectOutputs(state executionState, node Node, activation Activation) (json.RawMessage, error) {
	outputs := make([]json.RawMessage, 0, len(activation.EffectIDs))
	object := make(map[string]json.RawMessage, len(activation.EffectIDs))
	for _, effectID := range activation.EffectIDs {
		index := effectIndexByID(state, effectID)
		if index < 0 || state.Effects[index].Status != EffectCompleted || !json.Valid(state.Effects[index].Output) {
			return nil, ErrInvalidExecution
		}
		effect := state.Effects[index]
		outputs = append(outputs, cloneJSON(effect.Output))
		if effect.OutputKey != "" {
			object[effect.OutputKey] = cloneJSON(effect.Output)
		}
	}
	var value any
	switch node.Type {
	case NodeParallel:
		value = object
	case NodeMap:
		value = outputs
	case NodeEffect, NodeAgentTask, NodeApplicationEffect, NodeMediaEffect,
		NodeWait, NodeIf, NodeSubworkflow, NodeCompensation, NodeReturn:
		if len(outputs) != 1 {
			return nil, ErrInvalidExecution
		}
		return outputs[0], nil
	default:
		return nil, ErrInvalidExecution
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, errors.Join(ErrInvalidExecution, err)
	}
	return encoded, nil
}

func (runner *Runner) advanceNodeEffects(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	node Node,
	segment *segmentBudget,
) (kernel.Snapshot, bool, error) {
	if !nodeHasEffects(node) {
		return snapshot, true, ErrInvalidExecution
	}
	activationPosition := activationIndex(state, state.CurrentNode)
	if activationPosition < 0 {
		return snapshot, true, ErrInvalidExecution
	}
	activation := state.Activations[activationPosition]
	pending, err := pendingEffectIndices(state, activation)
	if err != nil {
		return snapshot, true, err
	}
	if len(pending) == 0 {
		output, aggregateErr := aggregateEffectOutputs(state, node, activation)
		if aggregateErr != nil {
			return snapshot, true, aggregateErr
		}
		return runner.finishEffectActivation(ctx, snapshot, state, node, activationPosition, output)
	}
	if segment.effects >= 1 {
		yielded, yieldErr := runner.yield(ctx, snapshot, state, "effect_budget")
		return yielded, true, yieldErr
	}
	segment.effects++
	outcomes := runner.executeEffectBatch(ctx, snapshot, state, pending, effectConcurrency(node))
	return runner.applyEffectOutcomes(ctx, snapshot, state, node, activationPosition, outcomes)
}

func (runner *Runner) applyEffectOutcomes(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	node Node,
	activationPosition int,
	outcomes []effectOutcome,
) (kernel.Snapshot, bool, error) {
	var failureCode string
	var failureDetail string
	var failureCause error
	retryScheduled := false
	for _, outcome := range outcomes {
		if outcome.EffectIndex < 0 || outcome.EffectIndex >= len(state.Effects) {
			return snapshot, true, ErrInvalidExecution
		}
		effect := &state.Effects[outcome.EffectIndex]
		if outcome.Err != nil {
			code := effectDispatchErrorCode(outcome.Err)
			if scheduleEffectRetry(effect, code, outcome.Err) {
				retryScheduled = true
				continue
			}
			markEffectFailedState(&state, outcome.EffectIndex, code, outcome.Err)
			if failureCause == nil {
				failureCode, failureDetail, failureCause = code, errorText(outcome.Err), outcome.Err
			}
			continue
		}
		effect.ChildRunID = strings.TrimSpace(outcome.Result.ChildRunID)
		if err := runner.ensureEffectRelation(ctx, snapshot.Run.ID, *effect); err != nil {
			if scheduleEffectRetry(effect, "workflow.effect_relation", err) {
				retryScheduled = true
				continue
			}
			markEffectFailedState(&state, outcome.EffectIndex, "workflow.effect_relation", err)
			if failureCause == nil {
				failureCode, failureDetail, failureCause = "workflow.effect_relation", errorText(err), err
			}
			continue
		}
		switch outcome.Result.Disposition {
		case DispositionPending:
			continue
		case DispositionCompleted:
			if strings.TrimSpace(outcome.Result.ReceiptID) == "" || !json.Valid(outcome.Result.Output) ||
				outcome.Result.CostUnits < 0 || outcome.Result.CostUnits > effect.MaxCostUnits {
				markEffectFailedState(&state, outcome.EffectIndex, "workflow.effect_receipt", ErrInvalidExecution)
				if failureCause == nil {
					failureCode, failureDetail, failureCause =
						"workflow.effect_receipt", ErrInvalidExecution.Error(), ErrInvalidExecution
				}
				continue
			}
			effect.Status = EffectCompleted
			effect.ReceiptID = strings.TrimSpace(outcome.Result.ReceiptID)
			effect.Output = cloneJSON(outcome.Result.Output)
			effect.ErrorCode = ""
			effect.Error = ""
			effect.CostUnits = outcome.Result.CostUnits
			state.Budget.CostUnitsReserved -= effect.MaxCostUnits
			state.Budget.CostUnitsUsed += effect.CostUnits
		case DispositionFailed:
			code := strings.TrimSpace(outcome.Result.ErrorCode)
			if code == "" {
				code = "workflow.effect_failed"
			}
			detail := strings.TrimSpace(outcome.Result.ErrorDetail)
			if detail == "" {
				detail = ErrEffectFailed.Error()
			}
			if scheduleEffectRetry(effect, code, errors.New(detail)) {
				retryScheduled = true
				continue
			}
			markEffectFailedState(&state, outcome.EffectIndex, code, errors.New(detail))
			if failureCause == nil {
				failureCode, failureDetail, failureCause = code, detail, ErrEffectFailed
			}
		default:
			markEffectFailedState(&state, outcome.EffectIndex, "workflow.effect_result", ErrInvalidExecution)
			if failureCause == nil {
				failureCode, failureDetail, failureCause =
					"workflow.effect_result", ErrInvalidExecution.Error(), ErrInvalidExecution
			}
		}
	}
	if failureCause != nil {
		activation := &state.Activations[activationPosition]
		activation.Status = ActivationFailed
		activation.ErrorCode = failureCode
		activation.Error = failureDetail
		abortActivationEffects(&state, *activation, "")
		failed, failErr := runner.beginFailure(
			ctx, snapshot, state, kernel.RunStatusFailed, failureCode,
			errors.Join(ErrEffectFailed, failureCause),
		)
		return failed, true, failErr
	}

	activation := state.Activations[activationPosition]
	pending, err := pendingEffectIndices(state, activation)
	if err != nil {
		return snapshot, true, err
	}
	if len(pending) > 0 {
		encoded, encodeErr := encodeExecutionState(state)
		if encodeErr != nil {
			return kernel.Snapshot{}, true, encodeErr
		}
		reason := "effect_pending"
		if retryScheduled {
			reason = "effect_retry_scheduled"
		}
		yielded, applyErr := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
			Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
			Events: []kernel.EventDraft{{
				Type: "workflow.segment.yielded", Message: reason, Wakeup: reason != "effect_pending",
			}},
		})
		return yielded, true, errors.Join(ErrEffectPending, applyErr)
	}
	output, err := aggregateEffectOutputs(state, node, activation)
	if err != nil {
		return runner.failAdvancedEffect(
			ctx, snapshot, state, activationPosition, -1, "workflow.effect_output", err,
		)
	}
	return runner.finishEffectActivation(ctx, snapshot, state, node, activationPosition, output)
}

func effectDispatchErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrEffectForbidden):
		return "workflow.effect_forbidden"
	case errors.Is(err, ErrEffectUnavailable):
		return "workflow.effect_unavailable"
	default:
		return "workflow.effect_dispatch"
	}
}

func scheduleEffectRetry(effect *Effect, code string, cause error) bool {
	if effect == nil || effect.Status != EffectPending || effect.Attempt >= effect.Retry.MaxAttempts ||
		!retryCodeAllowed(effect.Retry, code) {
		return false
	}
	effect.Attempt++
	effect.ErrorCode = strings.TrimSpace(code)
	effect.Error = errorText(cause)
	return true
}

func retryCodeAllowed(policy RetryPolicy, code string) bool {
	code = strings.TrimSpace(code)
	for _, allowed := range policy.RetryableErrorCodes {
		if allowed == "*" || allowed == code {
			return true
		}
	}
	return false
}

func markEffectFailedState(state *executionState, effectPosition int, code string, cause error) {
	if effectPosition < 0 || effectPosition >= len(state.Effects) {
		return
	}
	effect := &state.Effects[effectPosition]
	effect.Status = EffectFailed
	effect.ErrorCode = strings.TrimSpace(code)
	effect.Error = errorText(cause)
	state.Budget.CostUnitsReserved -= effect.MaxCostUnits
}

func (runner *Runner) finishEffectActivation(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	node Node,
	activationPosition int,
	output json.RawMessage,
) (kernel.Snapshot, bool, error) {
	state.Activations[activationPosition].Status = ActivationCompleted
	state.Activations[activationPosition].Output = cloneJSON(output)
	if node.Type == NodeCompensation {
		if err := runner.registerCompensation(&state, node, output); err != nil {
			failed, failErr := runner.beginFailure(
				ctx, snapshot, state, kernel.RunStatusFailed, "workflow.compensation_register", err,
			)
			return failed, true, failErr
		}
	}
	next, err := nextNodeIndex(state, node)
	if err != nil {
		failed, failErr := runner.beginFailure(
			ctx, snapshot, state, kernel.RunStatusFailed, "workflow.next_invalid", err,
		)
		return failed, true, failErr
	}
	state.CurrentNode = next
	encoded, err := encodeExecutionState(state)
	if err != nil {
		return kernel.Snapshot{}, true, err
	}
	completed, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{
			Type: "workflow.effect.completed", Message: state.Activations[activationPosition].ID,
		}},
	})
	return completed, false, err
}

func (runner *Runner) failAdvancedEffect(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	activationPosition int,
	effectPosition int,
	code string,
	cause error,
) (kernel.Snapshot, bool, error) {
	if effectPosition >= 0 && effectPosition < len(state.Effects) {
		effect := &state.Effects[effectPosition]
		effect.Status = EffectFailed
		effect.ErrorCode = code
		effect.Error = errorText(cause)
		state.Budget.CostUnitsReserved -= effect.MaxCostUnits
	}
	if activationPosition >= 0 && activationPosition < len(state.Activations) {
		state.Activations[activationPosition].Status = ActivationFailed
		state.Activations[activationPosition].ErrorCode = code
		state.Activations[activationPosition].Error = errorText(cause)
		abortActivationEffects(&state, state.Activations[activationPosition], "")
	}
	failed, err := runner.beginFailure(
		ctx, snapshot, state, kernel.RunStatusFailed, code, errors.Join(ErrEffectFailed, cause),
	)
	return failed, true, err
}

func abortActivationEffects(state *executionState, activation Activation, exceptEffectID string) {
	for _, effectID := range activation.EffectIDs {
		if effectID == exceptEffectID {
			continue
		}
		position := effectIndexByID(*state, effectID)
		if position < 0 || state.Effects[position].Status != EffectPending {
			continue
		}
		effect := &state.Effects[position]
		effect.Status = EffectFailed
		effect.ErrorCode = "workflow.effect_aborted"
		effect.Error = ErrEffectFailed.Error()
		state.Budget.CostUnitsReserved -= effect.MaxCostUnits
	}
}

func (runner *Runner) registerCompensation(state *executionState, node Node, output json.RawMessage) error {
	input, err := resolveValueSource(*state, node.Compensation.Undo.Input, nil, 0)
	if err != nil {
		// An undo may intentionally consume the completed do output from the current node.
		if node.Compensation.Undo.Input.Kind != ValueNodeOutput ||
			node.Compensation.Undo.Input.NodeID != node.ID {
			return err
		}
		input, err = resolveJSONPointer(output, node.Compensation.Undo.Input.Pointer)
		if err != nil {
			return err
		}
	}
	compensationID, err := runner.runtime.NewID("compensation")
	if err != nil {
		return err
	}
	state.Compensations = append(state.Compensations, Compensation{
		ID: compensationID, NodeID: node.ID, Call: node.Compensation.Undo,
		Input: cloneJSON(input), Status: CompensationPending,
	})
	return nil
}

func (runner *Runner) executeIf(
	ctx context.Context,
	snapshot kernel.Snapshot,
	state executionState,
	node Node,
) (kernel.Snapshot, bool, error) {
	condition, err := resolveValueSource(state, node.If.Condition, nil, 0)
	if err != nil {
		failed, failErr := runner.beginFailure(
			ctx, snapshot, state, kernel.RunStatusFailed, "workflow.condition_invalid", err,
		)
		return failed, true, failErr
	}
	var matched bool
	if err = json.Unmarshal(condition, &matched); err != nil {
		failed, failErr := runner.beginFailure(
			ctx, snapshot, state, kernel.RunStatusFailed, "workflow.condition_type", ErrInvalidExecution,
		)
		return failed, true, failErr
	}
	target := node.If.ElseNodeID
	if matched {
		target = node.If.ThenNodeID
	}
	next, err := nodeIndexByID(state.Definition, target)
	if err != nil {
		return snapshot, true, err
	}
	activationID, err := runner.runtime.NewID("activation")
	if err != nil {
		return kernel.Snapshot{}, true, err
	}
	state.Activations = append(state.Activations, Activation{
		ID: activationID, NodeID: node.ID, NodeIndex: state.CurrentNode,
		Status: ActivationCompleted, Attempt: 1, Output: cloneJSON(condition),
	})
	state.Budget.NodeActivations++
	state.CurrentNode = next
	encoded, err := encodeExecutionState(state)
	if err != nil {
		return kernel.Snapshot{}, true, err
	}
	advanced, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{Type: "workflow.branch.selected", Message: fmt.Sprintf("%s:%s", node.ID, target)}},
	})
	return advanced, false, err
}
