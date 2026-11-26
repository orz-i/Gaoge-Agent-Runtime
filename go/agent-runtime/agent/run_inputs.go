package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/callbackscope"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

const runInputBatchLimit = 16
const runInputTextLimit = 16 << 10
const runInputBatchBytes = 64 << 10

// ErrInputQueueFull describes Host queue capacity, not an exhausted Run budget.
var ErrInputQueueFull = errors.New("agent Host input queue is full")

// RunInput is authenticated Host user input, never a system instruction, Tool
// result, approval, model selection or permission grant. Sequence is assigned
// by the source; IDs and text must remain immutable within the original Run.
type RunInput struct {
	Sequence uint64
	ID       string
	Text     string
}

// RunInputSource is a trusted, read-only Host input log. ReadRunInputs must
// honor ctx, return contiguous records after the cursor, respect both the count
// and 64 KiB total-text bounds, and never dispatch external effects. The Host owns
// authentication, retention and durable storage. BindingID is immutable and
// nonsecret; it is an identity label, not a credential.
type RunInputSource interface {
	BindingID() string
	ReadRunInputs(context.Context, kernel.Run, uint64, int) ([]RunInput, error)
}

// The input read is an explicit synchronous Host stage, not a RunNext
// wrapper. This private journal identity witnesses the read's actual return
// only; it is never a callback-free or deferred-control qualification.
var inputReadStage = &struct{ marker bool }{true}

func (runner *Runner) readRunInputs(ctx context.Context, run kernel.Run, binding string, cursor uint64) (inputs []RunInput, err error) {
	source := struct {
		RunID            string
		Revision, Cursor uint64
		Binding          string
	}{run.ID, run.Revision, cursor, binding}
	readCtx, ticket := callbackscope.Enter(ctx, run.ID, inputReadStage, source)
	if ticket == nil {
		return nil, ErrInvalidRequest
	}
	defer func() {
		if failure := recover(); failure != nil {
			callbackscope.Aborted(ticket)
			panic(failure)
		}
		// The trusted port is read-only by contract. Its return is not a
		// receipt for any enclosing callback or input application CAS.
		if !callbackscope.Record(readCtx, inputReadStage, "input.read.returned", source) {
			err = errors.Join(err, ErrInvalidRequest)
		}
		callbackscope.Returned(ticket, err)
	}()
	if runner.rootCallbacks != nil {
		runner.rootCallbacks.inputReadEntered.Store(true)
	}
	if runner.inputs.BindingID() != binding {
		return nil, ErrInvalidRequest
	}
	return runner.inputs.ReadRunInputs(readCtx, run, cursor, runInputBatchLimit)
}

func validInputBinding(value string) bool {
	return value != "" && len(value) <= 128 && value == strings.TrimSpace(value) &&
		jsoncontract.ValidateCanonicalUTF8(value) == nil && !strings.ContainsAny(value, "\r\n\x00")
}

func validRunInput(input RunInput) bool {
	return validInputBinding(input.ID) && len(input.Text) <= runInputTextLimit &&
		strings.TrimSpace(input.Text) != "" && jsoncontract.ValidateCanonicalUTF8(input.Text) == nil
}

func (runner *Runner) admitRunInputs(ctx context.Context, snapshot kernel.Snapshot, state runState, previous *model.Response) (kernel.Snapshot, runState, bool, error) {
	if state.InputBinding == "" {
		return snapshot, state, false, nil // old Runs never silently enable inputs
	}
	frame, _ := callbackscope.RunCall(ctx).(*nativeRunAdmission)
	if snapshot.Run.ID == "" || snapshot.Run.Status != kernel.RunStatusRunning ||
		frame == nil || frame.invocation.RunID != snapshot.Run.ID || !frame.entered.Load() || frame.receiptOnly {
		return snapshot, state, false, ErrInvalidRequest
	}
	// The reducer may have consumed a final receipt in its local state, but
	// cannot read inputs over a pending model intent or outstanding Tool work.
	// Its original receipt and the input cursor will commit in the same CAS.
	if _, active := activeModelInvocation(state); active || len(state.PendingCalls) != 0 ||
		state.ToolDispatchID != "" || state.ToolWait != nil || len(state.DeferredChildren) != 0 ||
		state.PendingApproval != nil || state.ExternalToolCancellation != nil {
		return snapshot, state, false, ErrInvalidRequest
	}
	if runner.inputs == nil || runner.inputBinding != state.InputBinding {
		return snapshot, state, false, ErrInvalidRequest
	}
	if err := runner.requireNativeAdmission(ctx, snapshot.Run); err != nil {
		return snapshot, state, false, err
	}
	inputs, err := runner.readRunInputs(ctx, snapshot.Run, state.InputBinding, state.InputCursor)
	if err != nil {
		return snapshot, state, false, err
	}
	// Even an empty read is not authorization to finish a cancelled or
	// expired Run. The later mutation still uses the original revision CAS.
	if err = runner.requireNativeAdmission(ctx, snapshot.Run); err != nil {
		return snapshot, state, false, err
	}
	if len(inputs) == 0 {
		return snapshot, state, false, nil
	}
	if len(inputs) > runInputBatchLimit {
		return snapshot, state, false, ErrInvalidRequest
	}
	cursor, size := state.InputCursor, 0
	seen := make(map[string]bool, len(inputs))
	for _, input := range inputs {
		if cursor == ^uint64(0) || input.Sequence != cursor+1 || !validRunInput(input) || seen[input.ID] {
			return snapshot, state, false, ErrInvalidRequest
		}
		seen[input.ID] = true
		cursor = input.Sequence
		size += len(input.Text)
		if size > runInputBatchBytes {
			return snapshot, state, false, ErrInvalidRequest
		}
	}
	if previous != nil {
		state.Messages = append(state.Messages, model.Message{Role: model.RoleAssistant,
			Content: model.TextParts(previous.Content), ProviderReplay: model.CloneProviderReplay(previous.ProviderReplay)})
	}
	for _, input := range inputs {
		state.Messages = append(state.Messages, model.Message{Role: model.RoleUser, Content: model.TextParts(input.Text)})
	}
	state.InputCursor = cursor
	encoded, err := encodeState(state)
	if err != nil {
		return snapshot, state, false, err
	}
	mutation := kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{Type: "agent.input_applied", Message: "Host user input applied at a model boundary", Wakeup: true,
			WakeupAt: agentWakeupAt(runner.clock.Now())}},
	}
	next, err := runner.commitNativeTransition(ctx, snapshot, mutation)
	return next, state, true, err
}

// MemoryRunInputs is a bounded process-local example input log. It does not
// survive process exit. Use a Host durable RunInputSource for durable queued
// delivery. Queued is not applied: late input may require a new Host Turn.
type MemoryRunInputs struct {
	mu      sync.Mutex
	binding string
	logs    map[string][]RunInput
	bytes   int
}

func NewMemoryRunInputs(binding string) (*MemoryRunInputs, error) {
	if !validInputBinding(binding) {
		return nil, ErrInvalidRequest
	}
	return &MemoryRunInputs{binding: binding, logs: make(map[string][]RunInput)}, nil
}

func (queue *MemoryRunInputs) BindingID() string { return queue.binding }

func runInputScope(run kernel.Run) (string, error) {
	if run.Kind != RunKind || run.ID == "" || run.Actor.TenantID == "" || run.Actor.ActorID == "" || run.Thread.Kind == "" || run.Thread.ID == "" {
		return "", ErrInvalidRequest
	}
	value := struct {
		ID     string
		Actor  kernel.ActorRef
		Thread kernel.ThreadRef
	}{run.ID, run.Actor, run.Thread}
	if jsoncontract.ValidateCanonicalUTF8(value) != nil {
		return "", ErrInvalidRequest
	}
	encoded, err := json.Marshal(value)
	return string(encoded), err
}

// Submit appends an idempotent queued input after the Host authenticates and
// loads the original Run. The receipt does not claim atomicity with Run
// completion. Inspect View.InputCursor to determine whether it was applied.
func (queue *MemoryRunInputs) Submit(ctx context.Context, run kernel.Run, id, text string) (RunInput, error) {
	if err := ctx.Err(); err != nil {
		return RunInput{}, err
	}
	input := RunInput{ID: id, Text: text}
	scope, err := runInputScope(run)
	if err != nil || !validRunInput(input) || run.Status != kernel.RunStatusRunning {
		return RunInput{}, ErrInvalidRequest
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	for _, old := range queue.logs[scope] {
		if old.ID == id {
			if old.Text != text {
				return RunInput{}, kernel.ErrConflict
			}
			return old, nil
		}
	}
	if queue.bytes+len(text) > 1<<20 || len(queue.logs[scope]) >= 256 || len(queue.logs) >= 256 && len(queue.logs[scope]) == 0 {
		return RunInput{}, ErrInputQueueFull
	}
	input.Sequence = uint64(len(queue.logs[scope])) + 1
	queue.logs[scope] = append(queue.logs[scope], input)
	queue.bytes += len(text)
	return input, nil
}

// ReleaseRun frees a terminal Run's retained inputs and queue capacity. The
// Host calls it after draining submitters and finishing recovery or handoff of
// unapplied inputs. Releasing the same scope again is safe.
func (queue *MemoryRunInputs) ReleaseRun(ctx context.Context, run kernel.Run) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if run.Status != kernel.RunStatusCompleted && run.Status != kernel.RunStatusFailed && run.Status != kernel.RunStatusCancelled {
		return ErrInvalidRequest
	}
	scope, err := runInputScope(run)
	if err != nil {
		return err
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	for _, input := range queue.logs[scope] {
		queue.bytes -= len(input.Text)
	}
	delete(queue.logs, scope)
	return nil
}

func (queue *MemoryRunInputs) ReadRunInputs(ctx context.Context, run kernel.Run, after uint64, limit int) ([]RunInput, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	scope, err := runInputScope(run)
	if err != nil || limit < 1 || limit > runInputBatchLimit {
		return nil, ErrInvalidRequest
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	log := queue.logs[scope]
	if after > uint64(len(log)) {
		return nil, ErrInvalidRequest // lost history is not an empty successful read
	}
	var result []RunInput
	bytes := 0
	for _, input := range log[after:] {
		if len(result) == limit || bytes+len(input.Text) > runInputBatchBytes {
			break
		}
		result = append(result, input)
		bytes += len(input.Text)
	}
	return result, nil
}
