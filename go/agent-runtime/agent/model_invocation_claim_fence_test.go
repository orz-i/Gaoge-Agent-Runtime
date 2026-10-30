package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

type modelClaimClock struct{ nanos atomic.Int64 }

func newModelClaimClock() *modelClaimClock {
	clock := &modelClaimClock{}
	clock.nanos.Store(time.Date(2026, 10, 23, 8, 0, 0, 0, time.UTC).UnixNano())
	return clock
}
func (clock *modelClaimClock) Now() time.Time { return time.Unix(0, clock.nanos.Load()).UTC() }
func (clock *modelClaimClock) Advance(duration time.Duration) {
	clock.nanos.Add(duration.Nanoseconds())
}

type holdModelMiddleware struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (*holdModelMiddleware) Name() string { return "test.hold-model-after-claim" }
func (middleware *holdModelMiddleware) Model(ctx context.Context, req model.Request, emit model.StreamSink, next plugin.ModelNext) (model.Response, error) {
	middleware.once.Do(func() { close(middleware.entered) })
	select {
	case <-middleware.release:
	case <-ctx.Done():
		return model.Response{}, ctx.Err()
	}
	return next(ctx, req, emit)
}

// A queued/blocked ModelMiddleware can outlast the persisted claim lease.
// Once a new worker has claimed and completed that invocation, the old worker
// must not send a second physical Provider request with the same identity.
func TestExpiredModelInvocationClaimCannotDispatchAfterTakeover(t *testing.T) {
	clock := newModelClaimClock()
	store := memory.NewStore()
	runtime, err := kernel.New(kernel.Dependencies{Store: store, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	provider := &invocationRecordingModel{}
	hold := &holdModelMiddleware{entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(hold.release) }) }
	t.Cleanup(unblock)
	oldRunner, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Clock: clock, Model: provider, ModelMiddleware: []plugin.ModelMiddleware{hold}})
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		run kernel.Snapshot
		err error
	}
	outcomes := make(chan result, 1)
	go func() {
		run, e := oldRunner.StartRun(context.Background(), startRequest("model-claim-takeover", "claim-takeover-id", "finish"))
		outcomes <- result{run, e}
	}()
	select {
	case <-hold.entered:
	case <-time.After(4 * time.Second):
		t.Fatal("old claimed model never entered middleware")
	}
	claimed, err := runtime.Load(t.Context(), "model-claim-takeover")
	if err != nil {
		t.Fatal(err)
	}
	view := mustModelInvocationView(t, claimed)
	if len(view.ModelInvocations) != 1 || view.ModelInvocations[0].ExecutionLeaseUntil == nil || view.ModelInvocations[0].ExecutionAttempt != 1 {
		t.Fatalf("first claim was not persisted: %+v", view.ModelInvocations)
	}
	clock.Advance(3 * time.Minute)
	takeover, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Clock: clock, Model: provider})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := takeover.Resume(t.Context(), claimed.Run.ID, claimed.Run.Revision)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("new owner failed to complete: status=%s err=%v", completed.Run.Status, err)
	}
	unblock()
	select {
	case prior := <-outcomes:
		if prior.err == nil || (!errors.Is(prior.err, agent.ErrModelInvocationBusy) && !errors.Is(prior.err, kernel.ErrConflict)) {
			t.Fatalf("expired former owner succeeded: status=%s err=%v", prior.run.Run.Status, prior.err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("old owner remained blocked after release")
	}
	calls := provider.callsCopy()
	if len(calls) != 1 {
		t.Fatalf("expired claim caused %d physical Provider deliveries: %+v", len(calls), calls)
	}
}

type sharedLimiterModel struct {
	mu       sync.Mutex
	requests []model.Request
	entered  chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (client *sharedLimiterModel) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	client.mu.Lock()
	client.requests = append(client.requests, model.CloneRequest(request))
	client.mu.Unlock()
	if request.RunID == "model-limiter-holder" {
		client.once.Do(func() { close(client.entered) })
		select {
		case <-client.release:
		case <-ctx.Done():
			return model.Response{}, ctx.Err()
		}
	}
	return model.Response{Content: "complete"}, nil
}

// A claim occurs before the Runner's semaphore admission. A queued Run may
// outlive the claim lease while another Run occupies the sole model slot.
// The old queued request must not be sent after admission with an expired
// claim, even if no other worker has taken over its Run yet.
func TestModelLimiterWaitCannotDispatchExpiredClaim(t *testing.T) {
	clock := newModelClaimClock()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	provider := &sharedLimiterModel{entered: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(provider.release) }) }
	t.Cleanup(release)
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Clock: clock, Model: provider,
		Execution: agent.ExecutionPolicy{Model: agent.CallPolicy{MaxConcurrency: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		snapshot kernel.Snapshot
		err      error
	}
	holder := make(chan result, 1)
	go func() {
		snapshot, e := runner.StartRun(context.Background(), startRequest("model-limiter-holder", "limiter-holder", "hold"))
		holder <- result{snapshot, e}
	}()
	select {
	case <-provider.entered:
	case <-time.After(4 * time.Second):
		t.Fatal("holder never acquired Model slot")
	}
	waiting := make(chan result, 1)
	go func() {
		snapshot, e := runner.StartRun(context.Background(), startRequest("model-limiter-waiter", "limiter-waiter", "wait"))
		waiting <- result{snapshot, e}
	}()
	var claimed kernel.Snapshot
	deadline := time.After(4 * time.Second)
	for {
		latest, loadErr := runtime.Load(t.Context(), "model-limiter-waiter")
		if loadErr == nil {
			view, viewErr := agent.ViewState(latest)
			if viewErr == nil && len(view.ModelInvocations) == 1 && view.ModelInvocations[0].ExecutionLeaseUntil != nil {
				claimed = latest
				break
			}
		}
		select {
		case <-deadline:
			t.Fatal("waiting Model invocation never acquired a durable claim")
		case <-time.After(time.Millisecond):
		}
	}
	if claimed.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("waiting Run status=%s", claimed.Run.Status)
	}
	clock.Advance(3 * time.Minute)
	release()
	select {
	case done := <-holder:
		if done.err != nil || done.snapshot.Run.Status != kernel.RunStatusCompleted {
			t.Fatalf("holder failed: %+v", done)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("holder remained blocked")
	}
	select {
	case done := <-waiting:
		if !errors.Is(done.err, agent.ErrModelInvocationBusy) {
			t.Fatalf("expired waiting claim was dispatched: %+v", done)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("waiter remained blocked")
	}
	provider.mu.Lock()
	if len(provider.requests) != 1 || provider.requests[0].RunID != "model-limiter-holder" {
		t.Fatalf("expired queued request was physically sent: %+v", provider.requests)
	}
	provider.mu.Unlock()
	// The earlier stale attempt was blocked, not lost. Recovery after its
	// lease expires may claim a NEW attempt and dispatch the same identity.
	restored, err := runtime.Load(t.Context(), "model-limiter-waiter")
	if err != nil {
		t.Fatal(err)
	}
	completed, err := runner.Resume(t.Context(), restored.Run.ID, restored.Run.Revision)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("fresh claim failed to recover waiting Run: status=%s err=%v", completed.Run.Status, err)
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if len(provider.requests) != 2 || provider.requests[1].RunID != "model-limiter-waiter" ||
		provider.requests[1].InvocationID == "" {
		t.Fatalf("reclaimed invocation did not execute: %+v", provider.requests)
	}
}

type pausedHostedCatalog struct {
	tool      model.HostedTool
	mu        sync.Mutex
	calls     int
	blockCall int
	entered   chan struct{}
	release   chan struct{}
}

func (catalog *pausedHostedCatalog) Resolve(ctx context.Context, key, _ string) (model.HostedTool, bool, error) {
	if key != catalog.tool.Key {
		return model.HostedTool{}, false, nil
	}
	catalog.mu.Lock()
	catalog.calls++
	ordinal := catalog.calls
	catalog.mu.Unlock()
	if ordinal == catalog.blockCall {
		close(catalog.entered)
		select {
		case <-catalog.release:
		case <-ctx.Done():
			return model.HostedTool{}, false, ctx.Err()
		}
	}
	return model.CloneHostedTools([]model.HostedTool{catalog.tool})[0], true, nil
}

// Hosted Tool re-resolution can block *after* the original claim check.
// That asynchronous pause is still before the physical Model call. A new
// owner can take over during it, so claim/epoch must be checked after the
// Catalog guard, not before it.
func TestSlowHostedCatalogRecheckCannotDispatchStaleClaim(t *testing.T) {
	clock := newModelClaimClock()
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore(), Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	provider := &invocationRecordingModel{}
	tool := model.HostedTool{Key: "native.lookup", DefinitionVersion: "v1", Target: json.RawMessage(`{"native":true}`)}
	oldCatalog := &pausedHostedCatalog{tool: tool, blockCall: 4, entered: make(chan struct{}), release: make(chan struct{})}
	var once sync.Once
	unblock := func() { once.Do(func() { close(oldCatalog.release) }) }
	t.Cleanup(unblock)
	oldRunner, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Clock: clock, Model: provider, HostedTools: oldCatalog})
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		snapshot kernel.Snapshot
		err      error
	}
	outcomes := make(chan result, 1)
	go func() {
		snapshot, e := oldRunner.StartRun(context.Background(), startRequest("model-hosted-takeover", "hosted-takeover-req", "complete", "native.lookup"))
		outcomes <- result{snapshot, e}
	}()
	select {
	case <-oldCatalog.entered:
	case <-time.After(4 * time.Second):
		t.Fatal("final Catalog check was not reached")
	}
	claimed, err := runtime.Load(t.Context(), "model-hosted-takeover")
	if err != nil {
		t.Fatal(err)
	}
	view := mustModelInvocationView(t, claimed)
	if len(view.ModelInvocations) != 1 || view.ModelInvocations[0].ExecutionLeaseUntil == nil {
		t.Fatalf("unexpected claimed state %+v", view.ModelInvocations)
	}
	clock.Advance(3 * time.Minute)
	newCatalog := &pausedHostedCatalog{tool: tool}
	newRunner, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Clock: clock, Model: provider, HostedTools: newCatalog})
	if err != nil {
		t.Fatal(err)
	}
	current, err := newRunner.Resume(t.Context(), claimed.Run.ID, claimed.Run.Revision)
	if err != nil || current.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("new owner: %+v err=%v", current.Run, err)
	}
	unblock()
	select {
	case first := <-outcomes:
		if first.err == nil || (!errors.Is(first.err, kernel.ErrConflict) && !errors.Is(first.err, agent.ErrModelInvocationBusy)) {
			t.Fatalf("stale owner resumed physical dispatch: %+v err=%v", first.snapshot.Run, first.err)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("former worker blocked after Catalog release")
	}
	if calls := provider.callsCopy(); len(calls) != 1 {
		t.Fatalf("stale Catalog path physically dispatched again: %+v", calls)
	}
}

// Binding mutation after the claim but while the model request waits on the
// shared limiter must be rejected at the final Provider boundary. The earlier
// preclaim check is not sufficient, even for a previously frozen Provider name.
type mutableBindingLimiterModel struct {
	sharedLimiterModel
	binding atomic.Value // immutable hex fingerprint for each observed revision
}

// The first invocation uses real wall clock. Reclaim after the outstanding
// lease needs a compatible future clock, not an unrelated historical clock.
type postBindingLeaseClock struct{}

func (postBindingLeaseClock) Now() time.Time { return time.Now().UTC().Add(3 * time.Minute) }

func (*mutableBindingLimiterModel) ProviderName() string { return "same-provider" }

func (client *mutableBindingLimiterModel) BindingFingerprintForRequest(model.Request) string {
	return client.binding.Load().(string)
}

func TestModelLimiterWaitCannotDispatchChangedProviderBinding(t *testing.T) {
	runtime, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		t.Fatal(err)
	}
	provider := &mutableBindingLimiterModel{sharedLimiterModel: sharedLimiterModel{
		entered: make(chan struct{}), release: make(chan struct{}),
	}}
	provider.binding.Store(strings.Repeat("a", 64))
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(provider.release) }) }
	t.Cleanup(release)
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: provider,
		Execution: agent.ExecutionPolicy{Model: agent.CallPolicy{MaxConcurrency: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	type result struct {
		snapshot kernel.Snapshot
		err      error
	}
	holder := make(chan result, 1)
	go func() {
		snapshot, runErr := runner.StartRun(context.Background(),
			startRequest("model-limiter-holder", "binding-holder", "first"))
		holder <- result{snapshot, runErr}
	}()
	select {
	case <-provider.entered:
	case <-time.After(4 * time.Second):
		t.Fatal("first Provider request never occupied the limiter")
	}
	waiter := make(chan result, 1)
	go func() {
		snapshot, runErr := runner.StartRun(context.Background(),
			startRequest("model-limiter-waiter", "binding-waiter", "second"))
		waiter <- result{snapshot, runErr}
	}()
	var claimed kernel.Snapshot
	deadline := time.After(4 * time.Second)
	for {
		latest, loadErr := runtime.Load(t.Context(), "model-limiter-waiter")
		if loadErr == nil {
			view, viewErr := agent.ViewState(latest)
			if viewErr == nil && len(view.ModelInvocations) == 1 &&
				view.ModelInvocations[0].ExecutionLeaseUntil != nil {
				claimed = latest
				break
			}
		}
		select {
		case <-deadline:
			t.Fatal("waiting invocation never acquired its durable claim")
		case <-time.After(time.Millisecond):
		}
	}
	if claimed.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("unexpected pending status %s", claimed.Run.Status)
	}
	// The name is stable; only the effective binding changes *after* the
	// waiter has entered physical admission and blocked on the semaphore.
	provider.binding.Store(strings.Repeat("b", 64))
	release()
	select {
	case done := <-holder:
		if done.err != nil || done.snapshot.Run.Status != kernel.RunStatusCompleted {
			t.Fatalf("first request failed despite being physically in flight: %+v", done)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("first Provider request did not finish")
	}
	select {
	case done := <-waiter:
		if !errors.Is(done.err, agent.ErrProviderBindingMismatch) {
			t.Fatalf("changed binding reached Provider after permit wait: err=%v status=%s",
				done.err, done.snapshot.Run.Status)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("waiting request remained blocked after permit release")
	}
	provider.mu.Lock()
	requests := append([]model.Request(nil), provider.requests...)
	provider.mu.Unlock()
	if len(requests) != 1 || requests[0].RunID != "model-limiter-holder" {
		t.Fatalf("retargeted pending invocation physically sent: %+v", requests)
	}
	stale, err := runtime.Load(t.Context(), claimed.Run.ID)
	if err != nil || stale.Run.Revision != claimed.Run.Revision ||
		stale.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("denial changed committed pending work: %+v err=%v", stale.Run, err)
	}
	// A trusted operator can restore the original Host binding and reclaim
	// the still-pending logical attempt after the old lease expires, without
	// rewriting its intent or resurrecting the obsolete retargeted client.
	provider.binding.Store(strings.Repeat("a", 64))
	resumed, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: provider, Clock: postBindingLeaseClock{},
		Execution: agent.ExecutionPolicy{Model: agent.CallPolicy{MaxConcurrency: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	completed, err := resumed.Resume(t.Context(), stale.Run.ID, stale.Run.Revision)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("restored binding could not safely recover pending Run: status=%s err=%v", completed.Run.Status, err)
	}
	provider.mu.Lock()
	requests = append([]model.Request(nil), provider.requests...)
	provider.mu.Unlock()
	if len(requests) != 2 || requests[1].RunID != "model-limiter-waiter" {
		t.Fatalf("the original frozen request was not safely delivered after restoration: %+v", requests)
	}
}
