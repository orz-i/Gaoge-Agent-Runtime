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
	nonce                              uint64
	input                              [32]byte
	returned, failed, receipt, invalid bool
	proof                              [32]byte
	operations                         map[string][32]byte
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
	return context.WithValue(Bind(ctx, s), ticketKey{}, t)
}
func Enter(ctx context.Context, runID string, adapter any, input any) (context.Context, *Ticket) {
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
	t := &Ticket{scope: s, adapter: adapter, nonce: uint64(len(s.tickets) + 1), input: sha256.Sum256(raw), invalid: err != nil}
	s.tickets = append(s.tickets, t)
	return context.WithValue(ctx, ticketKey{}, t), t
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
