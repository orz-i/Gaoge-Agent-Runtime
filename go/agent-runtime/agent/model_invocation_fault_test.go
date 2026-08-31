package agent_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	runtimemodel "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

var (
	errInjectedModelInvocationCrash = errors.New("injected model invocation crash")
	errRetryableProvider            = errors.New("retryable provider failure")
)

func TestModelInvocationRecoversCrashAfterPendingCommitBeforeProviderCall(t *testing.T) {
	t.Parallel()
	base := memory.NewStore()
	faults := &modelInvocationFaultStore{
		Store: base, eventType: "agent.model_invocation.pending", afterCommit: true,
	}
	provider := &invocationRecordingModel{}
	runner := newModelInvocationRunner(t, faults, provider)
	_, err := runner.StartRun(t.Context(), startRequest("model-before-call", "req-before-call", "answer"))
	if !errors.Is(err, errInjectedModelInvocationCrash) {
		t.Fatalf("start error = %v", err)
	}
	if calls := provider.callsCopy(); len(calls) != 0 {
		t.Fatalf("provider called before pending intent recovery: %#v", calls)
	}

	runtime := newModelInvocationRuntime(t, base)
	pending := mustLoadModelInvocationRun(t, runtime, "model-before-call")
	assertInvocationState(t, pending, agent.ModelInvocationPending, 0)
	restarted := newModelInvocationRunnerWithClock(
		t, runtime, provider, modelInvocationClock{offset: 3 * time.Minute},
	)
	completed, err := restarted.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if completed.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("status = %s", completed.Run.Status)
	}
	calls := provider.callsCopy()
	if len(calls) != 1 || calls[0].InvocationID == "" {
		t.Fatalf("recovered provider calls = %#v", calls)
	}
	view := mustModelInvocationView(t, completed)
	assertConsumedInvocation(t, view, calls[0].InvocationID)
}

func TestModelInvocationRecoversCrashAfterExecutionClaimBeforeProviderCall(t *testing.T) {
	t.Parallel()
	base := memory.NewStore()
	faults := &modelInvocationFaultStore{
		Store: base, eventType: "agent.model_invocation.claimed", afterCommit: true,
	}
	provider := &invocationRecordingModel{}
	runner := newModelInvocationRunner(t, faults, provider)
	_, err := runner.StartRun(t.Context(), startRequest("model-after-claim", "req-after-claim", "answer"))
	if !errors.Is(err, errInjectedModelInvocationCrash) {
		t.Fatalf("start error = %v", err)
	}
	if calls := provider.callsCopy(); len(calls) != 0 {
		t.Fatalf("provider called after injected claim crash: %#v", calls)
	}

	runtime := newModelInvocationRuntime(t, base)
	claimed := mustLoadModelInvocationRun(t, runtime, "model-after-claim")
	assertInvocationState(t, claimed, agent.ModelInvocationPending, 0)
	restarted := newModelInvocationRunnerWithClock(
		t, runtime, provider, modelInvocationClock{offset: 3 * time.Minute},
	)
	completed, err := restarted.Resume(t.Context(), claimed.Run.ID, claimed.Run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	calls := provider.callsCopy()
	if len(calls) != 1 || calls[0].InvocationID == "" {
		t.Fatalf("recovered provider calls = %#v", calls)
	}
	assertConsumedInvocation(t, mustModelInvocationView(t, completed), calls[0].InvocationID)
}

func TestModelInvocationRecoversCrashAfterProviderResponseBeforeReceipt(t *testing.T) {
	t.Parallel()
	base := memory.NewStore()
	faults := &modelInvocationFaultStore{
		Store: base, eventType: "agent.model_invocation.completed", afterCommit: false,
	}
	provider := &invocationRecordingModel{}
	runner := newModelInvocationRunner(t, faults, provider)
	_, err := runner.StartRun(t.Context(), startRequest("model-before-receipt", "req-before-receipt", "answer"))
	if !errors.Is(err, errInjectedModelInvocationCrash) {
		t.Fatalf("start error = %v", err)
	}
	firstCalls := provider.callsCopy()
	if len(firstCalls) != 1 || firstCalls[0].InvocationID == "" {
		t.Fatalf("first provider calls = %#v", firstCalls)
	}

	runtime := newModelInvocationRuntime(t, base)
	pending := mustLoadModelInvocationRun(t, runtime, "model-before-receipt")
	assertInvocationState(t, pending, agent.ModelInvocationPending, 0)
	restarted := newModelInvocationRunnerWithClock(
		t, runtime, provider, modelInvocationClock{offset: 3 * time.Minute},
	)
	completed, err := restarted.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	calls := provider.callsCopy()
	if len(calls) != 2 || calls[0].InvocationID != calls[1].InvocationID {
		t.Fatalf("physical retries did not reuse logical invocation: %#v", calls)
	}
	view := mustModelInvocationView(t, completed)
	assertConsumedInvocation(t, view, calls[0].InvocationID)
}

func TestModelInvocationRecoversCrashAfterReceiptBeforeConsumption(t *testing.T) {
	t.Parallel()
	base := memory.NewStore()
	faults := &modelInvocationFaultStore{
		Store: base, eventType: "agent.model_invocation.completed", afterCommit: true,
	}
	provider := &invocationRecordingModel{}
	runner := newModelInvocationRunner(t, faults, provider)
	_, err := runner.StartRun(t.Context(), startRequest("model-after-receipt", "req-after-receipt", "answer"))
	if !errors.Is(err, errInjectedModelInvocationCrash) {
		t.Fatalf("start error = %v", err)
	}
	calls := provider.callsCopy()
	if len(calls) != 1 {
		t.Fatalf("provider calls = %#v", calls)
	}

	runtime := newModelInvocationRuntime(t, base)
	receipt := mustLoadModelInvocationRun(t, runtime, "model-after-receipt")
	assertInvocationState(t, receipt, agent.ModelInvocationCompleted, 0)
	restarted := newModelInvocationRunnerWithClock(
		t, runtime, provider, modelInvocationClock{offset: 3 * time.Minute},
	)
	completed, err := restarted.Resume(t.Context(), receipt.Run.ID, receipt.Run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(provider.callsCopy()); got != 1 {
		t.Fatalf("durable receipt caused provider replay: calls=%d", got)
	}
	view := mustModelInvocationView(t, completed)
	assertConsumedInvocation(t, view, calls[0].InvocationID)
}

func TestModelInvocationDuplicateExecutionConsumesOneLogicalReceipt(t *testing.T) {
	t.Parallel()
	base := memory.NewStore()
	faults := &modelInvocationFaultStore{
		Store: base, eventType: "agent.model_invocation.pending", afterCommit: true,
	}
	provider := newBlockingInvocationModel()
	preparing := newModelInvocationRunner(t, faults, provider)
	_, err := preparing.StartRun(t.Context(), startRequest("model-duplicate", "req-duplicate", "answer"))
	if !errors.Is(err, errInjectedModelInvocationCrash) {
		t.Fatalf("prepare error = %v", err)
	}
	runtime := newModelInvocationRuntime(t, base)
	pending := mustLoadModelInvocationRun(t, runtime, "model-duplicate")

	runnerA := newModelInvocationRunnerWithRuntime(t, runtime, provider)
	runnerB := newModelInvocationRunnerWithRuntime(t, runtime, provider)
	type resumeResult struct {
		snapshot kernel.Snapshot
		err      error
	}
	results := make(chan resumeResult, 1)
	go func() {
		snapshot, resumeErr := runnerA.Resume(context.Background(), pending.Run.ID, pending.Run.Revision)
		results <- resumeResult{snapshot: snapshot, err: resumeErr}
	}()
	select {
	case <-provider.entered:
	case <-time.After(time.Second):
		t.Fatal("first duplicate execution did not reach provider")
	}
	_, secondErr := runnerB.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
	if !errors.Is(secondErr, kernel.ErrConflict) {
		t.Fatalf("second duplicate execution error = %v", secondErr)
	}
	close(provider.release)
	first := <-results
	if first.err != nil {
		t.Fatalf("winning duplicate execution failed: %v", first.err)
	}
	calls := provider.callsCopy()
	if len(calls) != 1 || calls[0].InvocationID == "" {
		t.Fatalf("execution lease did not suppress duplicate provider call: %#v", calls)
	}
	final := mustLoadModelInvocationRun(t, runtime, pending.Run.ID)
	if final.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("final status = %s", final.Run.Status)
	}
	view := mustModelInvocationView(t, final)
	assertConsumedInvocation(t, view, calls[0].InvocationID)
}

func TestModelInvocationRetryableProviderErrorReusesPendingIntent(t *testing.T) {
	t.Parallel()
	base := memory.NewStore()
	runtime := newModelInvocationRuntime(t, base)
	provider := &retryableInvocationModel{}
	runner := newModelInvocationRunnerWithRuntime(t, runtime, provider)
	pending, err := runner.StartRun(t.Context(), startRequest("model-retryable", "req-retryable", "answer"))
	if !errors.Is(err, errRetryableProvider) {
		t.Fatalf("start error = %v", err)
	}
	if pending.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("retryable provider error made run terminal: %s", pending.Run.Status)
	}
	assertInvocationState(t, pending, agent.ModelInvocationPending, 0)
	completed, err := runner.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
	if err != nil {
		t.Fatal(err)
	}
	calls := provider.callsCopy()
	if len(calls) != 2 || calls[0].InvocationID == "" || calls[0].InvocationID != calls[1].InvocationID {
		t.Fatalf("retryable provider call changed invocation identity: %#v", calls)
	}
	view := mustModelInvocationView(t, completed)
	assertConsumedInvocation(t, view, calls[0].InvocationID)
}

func TestModelInvocationPersistsStreamingResponseMetadata(t *testing.T) {
	t.Parallel()
	base := memory.NewStore()
	runtime := newModelInvocationRuntime(t, base)
	provider := &streamingInvocationModel{}
	runner := newModelInvocationRunnerWithRuntime(t, runtime, provider)
	completed, err := runner.StartRun(t.Context(), startRequest("model-stream-metadata", "req-stream", "answer"))
	if err != nil {
		t.Fatal(err)
	}
	view := mustModelInvocationView(t, completed)
	if len(view.ModelInvocations) != 1 {
		t.Fatalf("model invocations = %#v", view.ModelInvocations)
	}
	invocation := view.ModelInvocations[0]
	if invocation.Provider != "fault-test-provider" || invocation.ProviderResponseID != "resp_stream_1" ||
		invocation.Response.ResponseID != "resp_stream_1" || invocation.Usage == nil ||
		invocation.Usage.InputTokens != 7 || invocation.Usage.OutputTokens != 3 ||
		invocation.Response.Usage == nil || invocation.Response.Usage.InputTokens != 7 {
		t.Fatalf("durable provider metadata = %#v", invocation)
	}
	if invocation.RequestHash == "" || invocation.SourceRevision == 0 || invocation.CreatedAt.IsZero() ||
		invocation.CompletedAt == nil || invocation.ConsumedAt == nil {
		t.Fatalf("durable invocation identity/timestamps = %#v", invocation)
	}
}

type modelInvocationFaultStore struct {
	kernel.Store
	mu          sync.Mutex
	eventType   string
	afterCommit bool
	failed      bool
}

func (store *modelInvocationFaultStore) Apply(
	ctx context.Context,
	runID string,
	expectedRevision uint64,
	mutation kernel.StoreMutation,
) (kernel.Snapshot, error) {
	store.mu.Lock()
	shouldFail := !store.failed && mutationHasEvent(mutation.Events, store.eventType)
	if shouldFail {
		store.failed = true
	}
	store.mu.Unlock()
	if !shouldFail {
		return store.Store.Apply(ctx, runID, expectedRevision, mutation)
	}
	if !store.afterCommit {
		return kernel.Snapshot{}, errInjectedModelInvocationCrash
	}
	snapshot, err := store.Store.Apply(ctx, runID, expectedRevision, mutation)
	if err != nil {
		return snapshot, err
	}
	return kernel.Snapshot{}, errInjectedModelInvocationCrash
}

func mutationHasEvent(events []kernel.EventDraft, eventType string) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

type invocationRecordingModel struct {
	mu    sync.Mutex
	calls []runtimemodel.Request
}

func (provider *invocationRecordingModel) ProviderName() string { return "fault-test-provider" }

func (provider *invocationRecordingModel) Generate(
	_ context.Context,
	request runtimemodel.Request,
) (runtimemodel.Response, error) {
	provider.mu.Lock()
	provider.calls = append(provider.calls, runtimemodel.CloneRequest(request))
	provider.mu.Unlock()
	return runtimemodel.Response{Content: "done", ResponseID: "resp_unary_1"}, nil
}

func (provider *invocationRecordingModel) callsCopy() []runtimemodel.Request {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	result := make([]runtimemodel.Request, len(provider.calls))
	for index, request := range provider.calls {
		result[index] = runtimemodel.CloneRequest(request)
	}
	return result
}

type blockingInvocationModel struct {
	invocationRecordingModel
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newBlockingInvocationModel() *blockingInvocationModel {
	return &blockingInvocationModel{entered: make(chan struct{}), release: make(chan struct{})}
}

func (provider *blockingInvocationModel) Generate(
	_ context.Context,
	request runtimemodel.Request,
) (runtimemodel.Response, error) {
	provider.mu.Lock()
	provider.calls = append(provider.calls, runtimemodel.CloneRequest(request))
	provider.mu.Unlock()
	provider.once.Do(func() { close(provider.entered) })
	<-provider.release
	return runtimemodel.Response{Content: "done", ResponseID: "resp_duplicate"}, nil
}

type retryableInvocationModel struct {
	invocationRecordingModel
}

func (provider *retryableInvocationModel) Generate(
	_ context.Context,
	request runtimemodel.Request,
) (runtimemodel.Response, error) {
	provider.mu.Lock()
	provider.calls = append(provider.calls, runtimemodel.CloneRequest(request))
	count := len(provider.calls)
	provider.mu.Unlock()
	if count == 1 {
		return runtimemodel.Response{}, runtimemodel.NewRetryableError(errRetryableProvider)
	}
	return runtimemodel.Response{Content: "done", ResponseID: "resp_retry"}, nil
}

type streamingInvocationModel struct{}

func (*streamingInvocationModel) ProviderName() string { return "fault-test-provider" }

func (*streamingInvocationModel) Generate(
	context.Context,
	runtimemodel.Request,
) (runtimemodel.Response, error) {
	return runtimemodel.Response{}, errors.New("streaming provider used unary path")
}

func (*streamingInvocationModel) GenerateStream(
	_ context.Context,
	_ runtimemodel.Request,
	emit func(runtimemodel.StreamEvent) error,
) (runtimemodel.Response, error) {
	if err := emit(runtimemodel.StreamEvent{
		ResponseID: "resp_stream_1",
		Usage:      &runtimemodel.Usage{InputTokens: 7, OutputTokens: 3},
	}); err != nil {
		return runtimemodel.Response{}, err
	}
	return runtimemodel.Response{Content: "done"}, nil
}

type modelInvocationClock struct {
	offset time.Duration
}

func (clock modelInvocationClock) Now() time.Time {
	return time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC).Add(clock.offset)
}

func newModelInvocationRuntime(t *testing.T, store kernel.Store) *kernel.Runtime {
	t.Helper()
	runtime, err := kernel.New(kernel.Dependencies{Store: store, Clock: modelInvocationClock{}})
	if err != nil {
		t.Fatal(err)
	}
	return runtime
}

func newModelInvocationRunner(t *testing.T, store kernel.Store, provider runtimemodel.Client) *agent.Runner {
	t.Helper()
	return newModelInvocationRunnerWithRuntime(t, newModelInvocationRuntime(t, store), provider)
}

func newModelInvocationRunnerWithRuntime(
	t *testing.T,
	runtime *kernel.Runtime,
	provider runtimemodel.Client,
) *agent.Runner {
	t.Helper()
	return newModelInvocationRunnerWithClock(t, runtime, provider, modelInvocationClock{})
}

func newModelInvocationRunnerWithClock(
	t *testing.T,
	runtime *kernel.Runtime,
	provider runtimemodel.Client,
	clock kernel.Clock,
) *agent.Runner {
	t.Helper()
	runner, err := agent.NewRunner(agent.Dependencies{
		Runtime: runtime, Model: provider, Clock: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func mustLoadModelInvocationRun(t *testing.T, runtime *kernel.Runtime, runID string) kernel.Snapshot {
	t.Helper()
	snapshot, err := runtime.Load(t.Context(), runID)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func mustModelInvocationView(t *testing.T, snapshot kernel.Snapshot) agent.View {
	t.Helper()
	view, err := agent.ViewState(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func assertInvocationState(
	t *testing.T,
	snapshot kernel.Snapshot,
	status agent.ModelInvocationStatus,
	llmCalls int,
) {
	t.Helper()
	view := mustModelInvocationView(t, snapshot)
	if view.LLMCalls != llmCalls || len(view.ModelInvocations) != 1 ||
		view.ModelInvocations[0].Status != status || view.ModelInvocations[0].ID == "" ||
		view.ModelInvocations[0].RequestHash == "" {
		t.Fatalf("model invocation view = %#v", view)
	}
}

func assertConsumedInvocation(t *testing.T, view agent.View, invocationID string) {
	t.Helper()
	if view.LLMCalls != 1 || len(view.ModelInvocations) != 1 {
		t.Fatalf("consumed invocation view = %#v", view)
	}
	invocation := view.ModelInvocations[0]
	if invocation.ID != invocationID || invocation.Status != agent.ModelInvocationConsumed ||
		invocation.CompletedAt == nil || invocation.ConsumedAt == nil || invocation.Response.Content != "done" {
		t.Fatalf("consumed invocation = %#v", invocation)
	}
}
