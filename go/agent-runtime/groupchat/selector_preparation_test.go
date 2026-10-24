package groupchat_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/continuation"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/groupchat"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

var errSelectorPrepareCommit = errors.New("selector preparation commit acknowledgement lost")
var errSelectorPrepareFailure = errors.New("selector preparation rejected")

type selectorPrepareClock struct{ now time.Time }

func (clock *selectorPrepareClock) Now() time.Time { return clock.now }

type selectorPreparationStore struct {
	kernel.Store
	failEvent string
	after     bool
}

func (store *selectorPreparationStore) Apply(ctx context.Context, runID string, revision uint64, mutation kernel.StoreMutation) (kernel.Snapshot, error) {
	for _, event := range mutation.Events {
		if event.Type != store.failEvent {
			continue
		}
		store.failEvent = ""
		if !store.after {
			return kernel.Snapshot{}, errSelectorPrepareCommit
		}
		snapshot, err := store.Store.Apply(ctx, runID, revision, mutation)
		if err == nil {
			err = errSelectorPrepareCommit
		}
		return snapshot, err
	}
	return store.Store.Apply(ctx, runID, revision, mutation)
}

type preparedSelector struct {
	prepare      func(context.Context, groupchat.SelectorRequest) (groupchat.SelectorRequest, error)
	prepareCalls int
	requests     []groupchat.SelectorRequest
	mutateInput  bool
}

func (selector *preparedSelector) PrepareSelectorRequest(ctx context.Context, request groupchat.SelectorRequest) (groupchat.SelectorRequest, error) {
	selector.prepareCalls++
	return selector.prepare(ctx, request)
}

func (selector *preparedSelector) Select(_ context.Context, request groupchat.SelectorRequest) (groupchat.SelectorResponse, error) {
	recorded := request
	recorded.PreparedInput = append(json.RawMessage(nil), request.PreparedInput...)
	selector.requests = append(selector.requests, recorded)
	if selector.mutateInput {
		request.PreparedInput[0] = '!'
	}
	return groupchat.SelectorResponse{SpeakerID: request.Candidates[0].ID}, nil
}

func newPreparedSelectorRunner(t *testing.T, store kernel.Store, clock *selectorPrepareClock, selector groupchat.Selector) (*kernel.Runtime, *groupchat.Runner) {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: store, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := groupchat.NewRunner(groupchat.Dependencies{
		Runtime: runtime, Handoffs: &recordingDelegator{}, Selector: selector,
		SelectorExecution: groupchat.SelectorExecutionPolicy{Timeout: 20 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	return runtime, runner
}

func frozenSelectorInvocation(t *testing.T, snapshot kernel.Snapshot) groupchat.SelectorInvocation {
	t.Helper()
	var state struct {
		SelectorInvocation groupchat.SelectorInvocation `json:"selectorInvocation"`
	}
	if err := json.Unmarshal(snapshot.State, &state); err != nil {
		t.Fatal(err)
	}
	return state.SelectorInvocation
}

func TestSelectorPreparedInputSurvivesRestartAndRemainsPrivate(t *testing.T) {
	for _, committedIntent := range []bool{true, false} {
		name := "pending intent committed"
		if !committedIntent {
			name = "selector response not committed"
		}
		t.Run(name, func(t *testing.T) {
			testPreparedSelectorRestart(t, committedIntent)
		})
	}
}

func testPreparedSelectorRestart(t *testing.T, committedIntent bool) {
	t.Helper()
	store := &selectorPreparationStore{Store: memory.NewStore(), failEvent: "groupchat.selector.created", after: true}
	if !committedIntent {
		store.failEvent, store.after = "groupchat.selector.completed", false
	}
	clock := &selectorPrepareClock{now: time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC)}
	payload := json.RawMessage(` { "route": "<PRIVATE_SELECTOR_INPUT>", "seed": 9007199254740993 } `)
	expected, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var preparedID string
	first := &preparedSelector{prepare: func(_ context.Context, request groupchat.SelectorRequest) (groupchat.SelectorRequest, error) {
		preparedID = request.InvocationID
		request.PreparedInput = payload
		return request, nil
	}}
	runtime, runner := newPreparedSelectorRunner(t, store, clock, first)
	request := selectorRequest()
	request.MaxUtterances = 1
	_, err = runner.StartRun(t.Context(), request)
	if !errors.Is(err, errSelectorPrepareCommit) || first.prepareCalls != 1 || preparedID == "" {
		t.Fatalf("preparation=%d id=%q err=%v", first.prepareCalls, preparedID, err)
	}
	pending, err := runtime.Load(t.Context(), request.ID)
	if err != nil {
		t.Fatal(err)
	}
	frozen := frozenSelectorInvocation(t, pending)
	if frozen.ID != preparedID || !bytes.Equal(frozen.Request.PreparedInput, expected) {
		t.Fatalf("pending input was not frozen: %+v", frozen)
	}
	assertPreparedSelectorPrivateAndTamperRejected(t, pending)
	payload[2] = '!'
	clock.now = clock.now.Add(3 * time.Minute)
	second := &preparedSelector{
		prepare: func(context.Context, groupchat.SelectorRequest) (groupchat.SelectorRequest, error) {
			return groupchat.SelectorRequest{}, errSelectorPrepareFailure
		},
		mutateInput: true,
	}
	_, restarted := newPreparedSelectorRunner(t, store.Store, clock, second)
	completed, err := restarted.Resume(t.Context(), request.ID, pending.Run.Revision)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted || second.prepareCalls != 0 || len(second.requests) != 1 {
		t.Fatalf("restart status=%s err=%v prepares=%d requests=%d", completed.Run.Status, err, second.prepareCalls, len(second.requests))
	}
	if !bytes.Equal(second.requests[0].PreparedInput, expected) || second.requests[0].InvocationID != preparedID {
		t.Fatalf("recovery changed input or identity: %+v", second.requests[0])
	}
	if len(first.requests) > 0 && !bytes.Equal(first.requests[0].PreparedInput, second.requests[0].PreparedInput) {
		t.Fatal("first dispatch and recovered dispatch received different payload bytes")
	}
	durable := frozenSelectorInvocation(t, completed)
	if !bytes.Equal(durable.Request.PreparedInput, expected) || durable.RequestHash != frozen.RequestHash {
		t.Fatal("selector mutation changed the durable request")
	}
}

func assertPreparedSelectorPrivateAndTamperRejected(t *testing.T, snapshot kernel.Snapshot) {
	t.Helper()
	view, err := groupchat.ViewState(snapshot)
	if err != nil || view.SelectorInvocation == nil || len(view.SelectorInvocation.Request.PreparedInput) != 0 {
		t.Fatalf("public view exposed private input: %+v err=%v", view, err)
	}
	public, err := json.Marshal(view)
	if err != nil || bytes.Contains(public, []byte("PRIVATE_SELECTOR_INPUT")) || bytes.Contains(public, []byte("preparedInput")) {
		t.Fatalf("private input crossed public view: %s err=%v", public, err)
	}
	tampered := snapshot
	tampered.State = bytes.Replace(snapshot.State, []byte("PRIVATE_SELECTOR_INPUT"), []byte("CHANGED_SELECTOR_INPUT"), 1)
	if _, err = groupchat.ViewState(tampered); !errors.Is(err, groupchat.ErrInvalidRequest) {
		t.Fatalf("tampered prepared input passed durable hash validation: %v", err)
	}
}

func TestSelectorPreparerCannotChangeLogicalAuthority(t *testing.T) {
	changes := map[string]func(*groupchat.SelectorRequest){
		"run":        func(request *groupchat.SelectorRequest) { request.RunID = "other-run" },
		"invocation": func(request *groupchat.SelectorRequest) { request.InvocationID = "other-invocation" },
		"model":      func(request *groupchat.SelectorRequest) { request.Model = "other-model" },
		"candidates": func(request *groupchat.SelectorRequest) { request.Candidates[0].ID = "unauthorized" },
		"goal":       func(request *groupchat.SelectorRequest) { request.Goal = "another goal" },
		"history": func(request *groupchat.SelectorRequest) {
			request.History = []groupchat.SelectorHistoryItem{{Content: "injected"}}
		},
		"completion":      func(request *groupchat.SelectorRequest) { request.CanComplete = !request.CanComplete },
		"options":         func(request *groupchat.SelectorRequest) { request.ModelOptions = json.RawMessage(`{"temperature":0}`) },
		"invalid payload": func(request *groupchat.SelectorRequest) { request.PreparedInput = json.RawMessage(`{`) },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			selector := &preparedSelector{prepare: func(_ context.Context, request groupchat.SelectorRequest) (groupchat.SelectorRequest, error) {
				change(&request)
				return request, nil
			}}
			_, runner := newPreparedSelectorRunner(t, memory.NewStore(), &selectorPrepareClock{now: time.Now().UTC()}, selector)
			snapshot, err := runner.StartRun(t.Context(), selectorRequest())
			if !errors.Is(err, groupchat.ErrInvalidRequest) || snapshot.Run.Status != kernel.RunStatusFailed || len(selector.requests) != 0 {
				t.Fatalf("invalid preparation dispatched: status=%s err=%v calls=%d", snapshot.Run.Status, err, len(selector.requests))
			}
			view, err := groupchat.ViewState(snapshot)
			if err != nil || view.SelectorInvocation != nil || view.CandidateSpeakerIDs[0] != "participant-researcher" {
				t.Fatalf("invalid preparation mutated logical state: %+v err=%v", view, err)
			}
		})
	}
}

func TestSelectorPreparationRetryRetainsContinuationDelivery(t *testing.T) {
	var identities []string
	selector := &preparedSelector{prepare: func(_ context.Context, request groupchat.SelectorRequest) (groupchat.SelectorRequest, error) {
		identities = append(identities, request.InvocationID)
		if len(identities) <= 2 {
			return groupchat.SelectorRequest{}, retryableSelectorError{}
		}
		request.PreparedInput = json.RawMessage(`{"route":"recovered"}`)
		return request, nil
	}}
	runtime, runner := newPreparedSelectorRunner(t, memory.NewStore(), &selectorPrepareClock{now: time.Now().UTC()}, selector)
	request := selectorRequest()
	request.MaxUtterances = 1
	pending, err := runner.StartRun(t.Context(), request)
	if !errors.Is(err, groupchat.ErrSelectorPending) {
		t.Fatalf("retryable preparation: %v", err)
	}
	dispatcher, err := continuation.NewDispatcher(runtime, continuation.RegisterResumer(groupchat.RunKind, runner))
	if err != nil {
		t.Fatal(err)
	}
	payload := continuation.Payload{
		RunID: request.ID, ExpectedRevision: pending.Run.Revision, Trigger: continuation.TriggerRunReady,
		SourceRunID: request.ID, SourceRevision: pending.Run.Revision,
	}
	if err = dispatcher.Dispatch(t.Context(), payload); !errors.Is(err, groupchat.ErrSelectorPending) {
		t.Fatalf("retryable preparation must retain the delivery for queue NACK: %v", err)
	}
	if err = dispatcher.Dispatch(t.Context(), payload); err != nil {
		t.Fatalf("same continuation could not recover: %v", err)
	}
	completed, err := runtime.Load(t.Context(), request.ID)
	if err != nil || completed.Run.Status != kernel.RunStatusCompleted || len(selector.requests) != 1 || len(identities) != 3 {
		t.Fatalf("recovery status=%s err=%v prepares=%d calls=%d", completed.Run.Status, err, len(identities), len(selector.requests))
	}
	if identities[0] == "" || identities[0] != identities[1] || identities[0] != identities[2] || selector.requests[0].InvocationID != identities[0] {
		t.Fatalf("preparation retry changed identity: %+v", identities)
	}
}

func TestSelectorPreparationFailureNeverCreatesIntentOrDispatches(t *testing.T) {
	cases := []struct {
		name    string
		prepare func(context.Context, context.CancelFunc) error
		want    error
		status  kernel.RunStatus
	}{
		{"permanent", func(context.Context, context.CancelFunc) error { return errSelectorPrepareFailure }, errSelectorPrepareFailure, kernel.RunStatusFailed},
		{"retryable", func(context.Context, context.CancelFunc) error { return retryableSelectorError{} }, groupchat.ErrSelectorPending, kernel.RunStatusRunning},
		{"cancelled", func(ctx context.Context, cancel context.CancelFunc) error { cancel(); return ctx.Err() }, context.Canceled, kernel.RunStatusRunning},
		{"timeout", func(ctx context.Context, _ context.CancelFunc) error { <-ctx.Done(); return ctx.Err() }, groupchat.ErrSelectorTimeout, kernel.RunStatusRunning},
		{"late success", func(ctx context.Context, _ context.CancelFunc) error { <-ctx.Done(); return nil }, groupchat.ErrSelectorTimeout, kernel.RunStatusRunning},
		{"panic", func(context.Context, context.CancelFunc) error { panic("private preparer failure") }, groupchat.ErrSelectorPanic, kernel.RunStatusFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			selector := &preparedSelector{prepare: func(ctx context.Context, request groupchat.SelectorRequest) (groupchat.SelectorRequest, error) {
				return request, tc.prepare(ctx, cancel)
			}}
			runtime, runner := newPreparedSelectorRunner(t, memory.NewStore(), &selectorPrepareClock{now: time.Now().UTC()}, selector)
			_, err := runner.StartRun(ctx, selectorRequest())
			if !errors.Is(err, tc.want) || len(selector.requests) != 0 {
				t.Fatalf("failure=%v calls=%d", err, len(selector.requests))
			}
			snapshot, err := runtime.Load(t.Context(), selectorRequest().ID)
			if err != nil || snapshot.Run.Status != tc.status {
				t.Fatalf("state=%+v err=%v", snapshot.Run, err)
			}
			view, err := groupchat.ViewState(snapshot)
			if err != nil || view.SelectorInvocation != nil {
				t.Fatalf("preparation failure created an invocation: %+v err=%v", view, err)
			}
		})
	}
}
