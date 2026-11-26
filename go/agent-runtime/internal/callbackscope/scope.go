// Package callbackscope is the Runtime-owned callback lifecycle journal. It is
// internal: host middleware cannot construct a layer receipt or a completion.
package callbackscope

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"sync"
)

type Binding struct {
	RunID, GrantID, OwnerID, LeaseID, SourceHash, Operation string
	Epoch                                                   uint64
}
type scopeKey struct{}
type ticketKey struct{}
type Scope struct {
	mu      sync.Mutex
	binding Binding
	trusted map[any]bool
	tickets []*Ticket
	closed  bool
}
type Ticket struct {
	scope                              *Scope
	adapter                            any
	toolCall                           any
	nonce                              uint64
	input                              [32]byte
	returned, failed, receipt, invalid bool
	proof                              [32]byte
	operations                         map[string][32]byte
	managedPreparing                   bool
	managedPrepared                    json.RawMessage
}

func New(binding Binding, trusted []any) *Scope {
	s := &Scope{binding: binding, trusted: make(map[any]bool)}
	for _, a := range trusted {
		s.trusted[a] = true
	}
	return s
}
func Bind(ctx context.Context, s *Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, s)
}
func Inherit(ctx, original context.Context) context.Context {
	if ctx == nil {
		ctx = original
	}
	s, _ := original.Value(scopeKey{}).(*Scope)
	t, _ := original.Value(ticketKey{}).(*Ticket)
	return BindRunCall(BindRootToolFrame(context.WithValue(Bind(ctx, s), ticketKey{}, t), RootToolFrame(original)), RunCall(original))
}

// Run-call authority belongs to the exact original native RunChain
// invocation. Context replacement cannot substitute a different source.
type runCallKey struct{}
type runCallValue struct{ authority any }

func BindRunCall(ctx context.Context, authority any) context.Context {
	return context.WithValue(ctx, runCallKey{}, runCallValue{authority})
}
func RunCall(ctx context.Context) any {
	value, _ := ctx.Value(runCallKey{}).(runCallValue)
	return value.authority
}

type rootToolFrameKey struct{}
type rootToolFrameValue struct{ frame any }

func BindRootToolFrame(ctx context.Context, frame any) context.Context {
	return context.WithValue(ctx, rootToolFrameKey{}, rootToolFrameValue{frame})
}
func RootToolFrame(ctx context.Context) any {
	value, _ := ctx.Value(rootToolFrameKey{}).(rootToolFrameValue)
	return value.frame
}

// Model-call authority is Runtime-owned and travels with the actual Next
// chain, even when a Host middleware supplies a replacement Context.
type modelCallKey struct{}

func BindModelCall(ctx context.Context, authority any) context.Context {
	return context.WithValue(ctx, modelCallKey{}, authority)
}
func ModelCall(ctx context.Context) any { return ctx.Value(modelCallKey{}) }
func InheritModel(ctx, original context.Context) context.Context {
	return BindModelCall(Inherit(ctx, original), ModelCall(original))
}

// Tool-call authority follows the actual Next chain even if middleware
// supplies an unrelated Context. It is independent of the whole Grant journal.
type toolCallKey struct{}

func BindToolCall(ctx context.Context, authority any) context.Context {
	return context.WithValue(ctx, toolCallKey{}, authority)
}
func ToolCall(ctx context.Context) any { return ctx.Value(toolCallKey{}) }
func InheritTool(ctx, original context.Context) context.Context {
	return BindToolCall(Inherit(ctx, original), ToolCall(original))
}

func Enter(ctx context.Context, runID string, adapter any, input any) (context.Context, *Ticket) {
	return enter(ctx, runID, adapter, input, nil)
}

// EnterTool binds only actual ToolChain layers to the original per-attempt
// authority, not active enclosing Run layers or nested foreign callbacks.
func EnterTool(ctx context.Context, runID string, adapter any, input any) (context.Context, *Ticket) {
	return enter(ctx, runID, adapter, input, ToolCall(ctx))
}
func enter(ctx context.Context, runID string, adapter any, input, toolCall any) (context.Context, *Ticket) {
	s, _ := ctx.Value(scopeKey{}).(*Scope)
	if s == nil {
		return ctx, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.binding.RunID != runID {
		return context.WithValue(Bind(ctx, nil), ticketKey{}, (*Ticket)(nil)), nil
	}
	raw, err := json.Marshal(input)
	t := &Ticket{scope: s, adapter: adapter, toolCall: toolCall, nonce: uint64(len(s.tickets) + 1), input: sha256.Sum256(raw), invalid: err != nil}
	s.tickets = append(s.tickets, t)
	return context.WithValue(ctx, ticketKey{}, t), t
}

// QualifiedToolReturn requires every actually entered layer of this exact
// Tool attempt to have returned its own trusted concrete receipt. A successful
// opaque layer is not a no-effect witness. The enclosing Run remains live;
// its whole-callback completion is still required for Grant retirement.
func QualifiedToolReturn(ctx context.Context) bool {
	s, _ := ctx.Value(scopeKey{}).(*Scope)
	call := ToolCall(ctx)
	if s == nil || call == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	found := false
	for _, t := range s.tickets {
		if t.toolCall != call {
			continue
		}
		found = true
		if t.invalid || !t.returned || !t.receipt || !s.trusted[t.adapter] {
			return false
		}
	}
	return found
}

// Record is callable only by audited Runtime implementations, at the end of
// their concrete operation sequence. It cannot accept a caller Completed flag.
func Record(ctx context.Context, adapter any, operation string, source any) bool {
	t, _ := ctx.Value(ticketKey{}).(*Ticket)
	if t == nil {
		return false
	}
	s := t.scope
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || t.returned || t.adapter != adapter || !s.trusted[adapter] || t.receipt || t.invalid || operation == "" {
		t.invalid = true
		return false
	}
	raw, err := json.Marshal(struct {
		Binding    Binding
		Nonce      uint64
		Input      [32]byte
		Operation  string
		Source     any
		Operations map[string][32]byte
	}{s.binding, t.nonce, t.input, operation, source, t.operations})
	if err != nil {
		t.invalid = true
		return false
	}
	t.proof = sha256.Sum256(raw)
	t.receipt = true
	return true
}

// Returned is called by the chain only after the callback and every entered
// Next have returned. A receipt alone cannot make a still-live layer complete.
func Returned(t *Ticket, err error) {
	if t == nil {
		return
	}
	s := t.scope
	s.mu.Lock()
	defer s.mu.Unlock()
	if t.returned {
		t.invalid = true
	}
	t.returned = true
	t.failed = err != nil
}
func (s *Scope) Complete() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	for _, t := range s.tickets {
		if t.invalid || !t.returned || (t.failed && !t.receipt) {
			return false
		}
	}
	return true
}
func (s *Scope) Close() {
	if s != nil {
		s.mu.Lock()
		s.closed = true
		s.mu.Unlock()
	}
}

// Operation records a completed concrete core operation in the current layer.
// It is not itself a callback receipt: actual Next and callback return are still
// required. Context calls this only after artifact persistence and checkpoint CAS.
func Operation(ctx context.Context, adapter any, identity string, source any) bool {
	t, _ := ctx.Value(ticketKey{}).(*Ticket)
	if t == nil {
		return false
	}
	s := t.scope
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || t.returned || t.receipt || t.invalid || t.adapter != adapter || !s.trusted[adapter] || identity == "" {
		t.invalid = true
		return false
	}
	raw, err := json.Marshal(source)
	if err != nil {
		t.invalid = true
		return false
	}
	hash := sha256.Sum256(raw)
	if t.operations == nil {
		t.operations = map[string][32]byte{}
	}
	if old, ok := t.operations[identity]; ok && old != hash {
		t.invalid = true
		return false
	}
	t.operations[identity] = hash
	return true
}

// Aborted prevents a panic after a receipt from masquerading as normal return.
func Aborted(t *Ticket) {
	if t != nil {
		t.scope.mu.Lock()
		t.invalid = true
		t.scope.mu.Unlock()
	}
}

// ManagedPrepareReturned records every successful concrete Context prepare
// path, including read-only early returns. It is NOT whole-callback completion.
func ManagedPrepareEntered(ctx context.Context, adapter any) {
	t, _ := ctx.Value(ticketKey{}).(*Ticket)
	if t == nil {
		return
	}
	s := t.scope
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || t.adapter != adapter || !s.trusted[adapter] || t.returned || t.managedPreparing {
		t.invalid = true
		return
	}
	t.managedPreparing = true
	t.managedPrepared = nil
}
func ManagedPrepareReturned(ctx context.Context, adapter any, request any, err error) bool {
	t, _ := ctx.Value(ticketKey{}).(*Ticket)
	if t == nil {
		return false
	}
	s := t.scope
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || t.adapter != adapter || !s.trusted[adapter] || t.returned || !t.managedPreparing || err != nil {
		t.invalid = true
		return false
	}
	raw, e := json.Marshal(request)
	if e != nil {
		t.invalid = true
		return false
	}
	t.managedPreparing = false
	t.managedPrepared = raw
	return true
}

// ManagedSuspension is available only inside the one audited Context Run
// frame. Every other entered ticket must have returned without an unresolved
// failure. The output journals actual operations, never a caller promise.
func ManagedSuspension(ctx context.Context, adapter any, request any) (json.RawMessage, bool) {
	return managedSuspension(ctx, adapter, nil, request, "", false)
}

// ManagedObligationSuspension accepts only the explicitly selected native
// Budget frame with an actual admission operation and no unresolved ticket.
// Summary supplies its concrete phase operation instead of a business prepare.
func ManagedObligationSuspension(ctx context.Context, adapter, budgetAdapter any, request any, phase string) (json.RawMessage, bool) {
	if phase != "" {
		phase = "context.summary.native-phase:" + phase
	}
	return managedSuspension(ctx, adapter, budgetAdapter, request, phase, true)
}

// ManagedPreparationSuspension proves the actual hydrated native frontier
// before model preparation. It shares the same complete-ticket and Budget
// admission checks as the concrete model/summary safe points.
func ManagedPreparationSuspension(ctx context.Context, adapter, budgetAdapter any, identity string, source any) (json.RawMessage, bool) {
	operation := "managed.preparation:" + identity
	if !Operation(ctx, adapter, operation, source) {
		return nil, false
	}
	return managedSuspension(ctx, adapter, budgetAdapter, source, operation, true)
}

func managedSuspension(ctx context.Context, adapter, budgetAdapter any, request any, phase string, obligations bool) (json.RawMessage, bool) {
	t, _ := ctx.Value(ticketKey{}).(*Ticket)
	if t == nil {
		return nil, false
	}
	s := t.scope
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || t.adapter != adapter || !s.trusted[adapter] || t.returned || t.invalid || t.receipt {
		return nil, false
	}
	if _, ok := t.operations["managed.hydrated"]; !ok {
		return nil, false
	}
	if t.managedPreparing {
		return nil, false
	}
	if phase == "" {
		if len(t.managedPrepared) == 0 {
			return nil, false
		}
	} else if _, ok := t.operations[phase]; !ok {
		return nil, false
	}
	var budgetProof *[32]byte
	for _, other := range s.tickets {
		if other == t {
			continue
		}
		if obligations && budgetAdapter != nil && other.adapter == budgetAdapter && !other.returned {
			if other.invalid || other.receipt || !s.trusted[other.adapter] || budgetProof != nil {
				return nil, false
			}
			proof, ok := other.operations["managed.budget.admitted"]
			if !ok {
				return nil, false
			}
			budgetProof = &proof
			continue
		}
		if other.invalid || !other.returned || (other.failed && !other.receipt) ||
			(obligations && !s.trusted[other.adapter]) {
			return nil, false
		}
	}
	if obligations && (budgetAdapter != nil) != (budgetProof != nil) {
		return nil, false
	}
	b, e := json.Marshal(struct {
		Binding         Binding
		Nonce           uint64
		Input           [32]byte
		Operations      map[string][32]byte
		Request         any
		Prepared        json.RawMessage
		BudgetAdmission *[32]byte `json:",omitempty"`
	}{s.binding, t.nonce, t.input, t.operations, request, t.managedPrepared, budgetProof})
	return b, e == nil
}

// EnterPreparation creates a separate synchronous ticket, never borrowing the
// innermost Budget/Host ticket. The selected concrete Context Run must actually
// be active and have completed hydration in this very scope.
func EnterPreparation(ctx context.Context, runID string, adapter any, input any) (context.Context, *Ticket) {
	s, _ := ctx.Value(scopeKey{}).(*Scope)
	if s == nil {
		return ctx, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.binding.RunID != runID || !s.trusted[adapter] {
		return ctx, nil
	}
	active := 0
	for _, t := range s.tickets {
		if t.adapter == adapter && !t.returned && !t.invalid && !t.receipt {
			if _, ok := t.operations["managed.hydrated"]; ok {
				active++
			}
		}
	}
	if active != 1 {
		return ctx, nil
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return ctx, nil
	}
	t := &Ticket{scope: s, adapter: adapter, nonce: uint64(len(s.tickets) + 1), input: sha256.Sum256(raw)}
	s.tickets = append(s.tickets, t)
	return context.WithValue(ctx, ticketKey{}, t), t
}
