package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const (
	agentCrashExitCode = 77
	agentCrashRunID    = "agent-process-crash"
)

func TestRealPostgresAgentRecoveryAcrossProcessCrash(t *testing.T) {
	dsn := requireRecoveryPostgres(t)
	cases := []struct {
		name        string
		event       string
		afterCommit bool
		beforeCalls int
		totalCalls  int
		status      agent.ModelInvocationStatus
	}{
		{"pending_committed", "agent.model_invocation.pending", true, 0, 1, agent.ModelInvocationPending},
		{"claim_committed", "agent.model_invocation.claimed", true, 0, 1, agent.ModelInvocationPending},
		{"response_before_receipt", "agent.model_invocation.completed", false, 1, 2, agent.ModelInvocationPending},
		{"receipt_committed", "agent.model_invocation.completed", true, 1, 1, agent.ModelInvocationCompleted},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			db, isolatedDSN := openIsolatedRealPostgres(t, dsn)
			if err := Migrate(db); err != nil {
				t.Fatal(err)
			}
			provider := newRecoveryModel(t, nil)
			runAgentCrashProcess(t, isolatedDSN, provider.endpoint, test.event, test.afterCommit)

			store := NewKernelStore(db)
			pending, err := store.Load(t.Context(), agentCrashRunID)
			if err != nil {
				t.Fatal(err)
			}
			view := recoveryAgentView(t, pending)
			if len(view.ModelInvocations) != 1 || view.ModelInvocations[0].Status != test.status ||
				view.Budget.Usage.LLMCalls != 0 || pending.Run.Status != kernel.RunStatusRunning {
				t.Fatalf("state after process exit: snapshot=%#v view=%#v", pending, view)
			}
			if calls := provider.calls(); len(calls) != test.beforeCalls {
				t.Fatalf("physical calls before recovery = %d, want %d", len(calls), test.beforeCalls)
			}
			invocationID := view.ModelInvocations[0].ID
			if invocationID == "" {
				t.Fatal("pending invocation has no durable identity")
			}

			// Only the database and the external HTTP server survive the child.
			// Advance the injected clock past the dead process's execution lease.
			runner, _ := newRecoveryAgent(t, store, provider, 3*time.Minute)
			completed, err := runner.Resume(t.Context(), pending.Run.ID, pending.Run.Revision)
			if err != nil {
				t.Fatal(err)
			}
			assertRecoveredAgent(t, completed, invocationID, test.totalCalls)
			for _, request := range provider.calls() {
				if request.InvocationID != invocationID || request.RunID != pending.Run.ID {
					t.Fatalf("physical retry changed invocation identity: %#v", request)
				}
			}
			if calls := provider.calls(); len(calls) != test.totalCalls {
				t.Fatalf("physical calls after recovery = %d, want %d", len(calls), test.totalCalls)
			}
			persisted, err := store.Load(t.Context(), pending.Run.ID)
			if err != nil {
				t.Fatal(err)
			}
			assertRecoveredAgent(t, persisted, invocationID, test.totalCalls)
			if _, err = runner.Resume(t.Context(), persisted.Run.ID, persisted.Run.Revision); !errors.Is(err, agent.ErrRunTerminal) {
				t.Fatalf("terminal replay error = %v", err)
			}
			if len(provider.calls()) != test.totalCalls {
				t.Fatal("terminal replay dispatched another physical call")
			}
		})
	}
}

// TestAgentRecoveryCrashProcess runs only inside the subprocess above. os.Exit
// deliberately bypasses Go defers and runner cleanup at a persistence boundary.
func TestAgentRecoveryCrashProcess(t *testing.T) {
	event := os.Getenv("GAOGE_TEST_AGENT_CRASH_EVENT")
	if event == "" {
		t.Skip("subprocess helper")
	}
	db := reopenRecoveryPostgres(t, requireRecoveryPostgres(t))
	store := &agentCrashStore{
		Store: NewKernelStore(db), event: event,
		afterCommit: os.Getenv("GAOGE_TEST_AGENT_CRASH_AFTER_COMMIT") == "true",
	}
	provider := &recoveryHTTPModel{
		endpoint: os.Getenv("GAOGE_TEST_AGENT_PROVIDER_URL"),
		client:   &http.Client{Timeout: 10 * time.Second},
	}
	runner, _ := newRecoveryAgent(t, store, provider, 0)
	_, err := runner.StartRun(t.Context(), recoveryStartRequest())
	t.Fatalf("process did not exit at %s: %v", event, err)
}

func TestRealPostgresAgentCancellationFencesLateModelReceipt(t *testing.T) {
	db, isolatedDSN := openIsolatedRealPostgres(t, requireRecoveryPostgres(t))
	if err := Migrate(db); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	provider := newRecoveryModel(t, release)
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(unblock)
	runner, _ := newRecoveryAgent(t, NewKernelStore(db), provider, 0)
	result := make(chan error, 1)
	go func() {
		_, err := runner.StartRun(t.Context(), recoveryStartRequest())
		result <- err
	}()
	select {
	case <-provider.entered:
	case <-time.After(10 * time.Second):
		t.Fatal("model call did not reach the external server")
	}

	// A separate connection and runtime cancel while the provider is in flight.
	otherStore := NewKernelStore(reopenRecoveryPostgres(t, isolatedDSN))
	otherRunner, otherRuntime := newRecoveryAgent(t, otherStore, provider, 0)
	inFlight, err := otherStore.Load(t.Context(), agentCrashRunID)
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := otherRuntime.Cancel(t.Context(), inFlight.Run.ID, inFlight.Run.Revision, "cancel in flight")
	if err != nil {
		t.Fatal(err)
	}
	unblock()
	select {
	case err = <-result:
		if !errors.Is(err, kernel.ErrConflict) && !errors.Is(err, kernel.ErrTerminal) {
			t.Fatalf("late model response error = %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("model caller did not stop after cancellation")
	}
	final, err := otherStore.Load(t.Context(), inFlight.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	view := recoveryAgentView(t, final)
	if final.Run.Status != kernel.RunStatusCancelled || final.Run.Revision != cancelled.Run.Revision ||
		final.EventHead != cancelled.EventHead || final.Result != nil || view.Budget.Usage.LLMCalls != 0 ||
		len(view.ModelInvocations) != 1 || view.ModelInvocations[0].Status != agent.ModelInvocationPending {
		t.Fatalf("late receipt changed the cancelled run: snapshot=%#v view=%#v", final, view)
	}
	if _, err = otherRunner.Resume(t.Context(), final.Run.ID, final.Run.Revision); !errors.Is(err, agent.ErrRunTerminal) {
		t.Fatalf("cancelled replay error = %v", err)
	}
	if len(provider.calls()) != 1 {
		t.Fatal("cancelled run dispatched another model call")
	}
}

func requireRecoveryPostgres(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}
	return dsn
}

func reopenRecoveryPostgres(t *testing.T, dsn string) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(pgdriver.Open(dsn), realPostgresTestConfig())
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

func runAgentCrashProcess(t *testing.T, dsn, endpoint, event string, afterCommit bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestAgentRecoveryCrashProcess$", "-test.count=1")
	command.Env = append(os.Environ(),
		"TEST_POSTGRES_DSN="+dsn,
		"GAOGE_TEST_AGENT_PROVIDER_URL="+endpoint,
		"GAOGE_TEST_AGENT_CRASH_EVENT="+event,
		"GAOGE_TEST_AGENT_CRASH_AFTER_COMMIT="+strconv.FormatBool(afterCommit),
	)
	output, err := command.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != agentCrashExitCode {
		t.Fatalf("crash process: %v\n%s", err, output)
	}
}

type agentCrashStore struct {
	kernel.Store
	event       string
	afterCommit bool
}

func (store *agentCrashStore) Apply(ctx context.Context, runID string, revision uint64, mutation kernel.StoreMutation) (kernel.Snapshot, error) {
	crash := false
	for _, event := range mutation.Events {
		crash = crash || event.Type == store.event
	}
	if crash && !store.afterCommit {
		os.Exit(agentCrashExitCode)
	}
	snapshot, err := store.Store.Apply(ctx, runID, revision, mutation)
	if crash && store.afterCommit && err == nil {
		os.Exit(agentCrashExitCode)
	}
	return snapshot, err
}

type recoveryClock struct{ now time.Time }

func (clock recoveryClock) Now() time.Time { return clock.now }

func newRecoveryAgent(t *testing.T, store kernel.Store, provider model.Client, offset time.Duration) (*agent.Runner, *kernel.Runtime) {
	t.Helper()
	clock := recoveryClock{now: kernelTestNow().Add(offset)}
	runtime, err := kernel.New(kernel.Dependencies{Store: store, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: provider, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	return runner, runtime
}

func recoveryStartRequest() agent.StartRequest {
	return agent.StartRequest{
		ID: agentCrashRunID, RequestID: "recovery-request", Goal: "verify durable recovery",
		Actor:  kernel.ActorRef{TenantID: "tenant", ActorID: "actor"},
		Thread: kernel.ThreadRef{Kind: "conversation", ID: "thread"},
	}
}

func recoveryAgentView(t *testing.T, snapshot kernel.Snapshot) agent.View {
	t.Helper()
	view, err := agent.ViewState(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func assertRecoveredAgent(t *testing.T, snapshot kernel.Snapshot, invocationID string, physicalCalls int) {
	t.Helper()
	view := recoveryAgentView(t, snapshot)
	if snapshot.Run.Status != kernel.RunStatusCompleted || snapshot.Result == nil || len(view.ModelInvocations) != 1 {
		t.Fatalf("recovered snapshot=%#v view=%#v", snapshot, view)
	}
	invocation := view.ModelInvocations[0]
	if invocation.ID != invocationID || invocation.Status != agent.ModelInvocationConsumed ||
		invocation.CompletedAt == nil || invocation.ConsumedAt == nil || invocation.Response.Content != "done" ||
		invocation.ProviderResponseID != fmt.Sprintf("response-%d", physicalCalls) {
		t.Fatalf("recovered receipt = %#v", invocation)
	}
	usage := view.Budget.Usage
	if usage.LLMCalls != 1 || usage.InputTokens != 7 || usage.OutputTokens != 3 || usage.TotalTokens != 10 || usage.ReasoningTokens != 2 {
		t.Fatalf("receipt usage was not consumed once: %#v", usage)
	}
	answers := 0
	for _, message := range view.Messages {
		if message.Role == "assistant" && message.Content == "done" {
			answers++
		}
	}
	if answers != 1 {
		t.Fatalf("receipt produced %d assistant messages, want one", answers)
	}
}

type recoveryHTTPModel struct {
	endpoint string
	client   *http.Client
}

func (provider *recoveryHTTPModel) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	encoded, err := json.Marshal(request)
	if err != nil {
		return model.Response{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, provider.endpoint, bytes.NewReader(encoded))
	if err != nil {
		return model.Response{}, err
	}
	response, err := provider.client.Do(httpRequest)
	if err != nil {
		return model.Response{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return model.Response{}, fmt.Errorf("test provider returned HTTP %d", response.StatusCode)
	}
	var result model.Response
	err = json.NewDecoder(response.Body).Decode(&result)
	return result, err
}

type recordingRecoveryModel struct {
	recoveryHTTPModel
	mu       sync.Mutex
	requests []model.Request
	entered  chan struct{}
	once     sync.Once
}

func newRecoveryModel(t *testing.T, pause <-chan struct{}) *recordingRecoveryModel {
	t.Helper()
	provider := &recordingRecoveryModel{entered: make(chan struct{})}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var call model.Request
		if err := json.NewDecoder(request.Body).Decode(&call); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		provider.mu.Lock()
		provider.requests = append(provider.requests, call)
		callNumber := len(provider.requests)
		provider.mu.Unlock()
		provider.once.Do(func() { close(provider.entered) })
		if pause != nil {
			select {
			case <-pause:
			case <-request.Context().Done():
				return
			}
		}
		if err := json.NewEncoder(writer).Encode(model.Response{
			Content: "done", ResponseID: fmt.Sprintf("response-%d", callNumber),
			Usage: &model.Usage{InputTokens: 7, OutputTokens: 3, ReasoningTokens: 2},
		}); err != nil {
			t.Errorf("write test provider response: %v", err)
		}
	}))
	t.Cleanup(server.Close)
	provider.recoveryHTTPModel = recoveryHTTPModel{endpoint: server.URL, client: &http.Client{Timeout: 10 * time.Second}}
	return provider
}

func (provider *recordingRecoveryModel) calls() []model.Request {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return append([]model.Request(nil), provider.requests...)
}
