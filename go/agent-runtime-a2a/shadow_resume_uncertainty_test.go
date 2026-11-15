package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

// This fake intentionally does NOT deduplicate the physical A2A MessageID.
// A remote implementation is not required to deduplicate repeated Sends.
type uncertainResumeClient struct {
	mu         sync.Mutex
	task       TaskSnapshot
	sends      []SendRequest
	gets       int
	cancels    int
	resumeErr  error
	resumeMode string
	entered    chan struct{}
	release    chan struct{}
}

func newUncertainResumeClient() *uncertainResumeClient {
	return &uncertainResumeClient{task: inputRequiredTask()}
}

func (remote *uncertainResumeClient) SendMessage(ctx context.Context, _ Discovery, request SendRequest) (Interaction, error) {
	remote.mu.Lock()
	remote.sends = append(remote.sends, request)
	task, sendErr, mode := cloneTaskValue(remote.task), remote.resumeErr, remote.resumeMode
	entered, release := remote.entered, remote.release
	remote.mu.Unlock()
	if request.TaskID == "" {
		return Interaction{Task: &task, Raw: task.Raw}, nil
	}
	if entered != nil {
		select {
		case entered <- struct{}{}:
		default:
		}
	}
	if release != nil {
		select {
		case <-release:
		case <-ctx.Done():
			return Interaction{}, ctx.Err()
		}
	}
	if sendErr != nil {
		return Interaction{}, sendErr
	}
	switch mode {
	case "malformed":
		return Interaction{Task: &task, Message: &MessageSnapshot{ID: "ambiguous"}}, nil
	case "foreign":
		task.ContextID = "foreign-context"
		return Interaction{Task: &task, Raw: task.Raw}, nil
	case "unknown":
		return Interaction{}, nil
	default:
		return Interaction{Task: &task, Raw: task.Raw}, nil
	}
}

func (remote *uncertainResumeClient) GetTask(_ context.Context, _ Discovery, _ string) (TaskSnapshot, error) {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	remote.gets++
	return cloneTaskValue(remote.task), nil
}

func (remote *uncertainResumeClient) CancelTask(_ context.Context, _ Discovery, _ string) (TaskSnapshot, error) {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	remote.cancels++
	remote.task = completedTask()
	remote.task.State = "TASK_STATE_CANCELED"
	remote.task.Terminal = true
	remote.task.Raw = json.RawMessage(`{"id":"remote-1","contextId":"context-1","status":{"state":"TASK_STATE_CANCELED"}}`)
	return cloneTaskValue(remote.task), nil
}

func (remote *uncertainResumeClient) setTask(task TaskSnapshot) {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	remote.task = task
}

func (remote *uncertainResumeClient) counts() (sends, resumes, gets, cancels int) {
	remote.mu.Lock()
	defer remote.mu.Unlock()
	for _, send := range remote.sends {
		if send.TaskID != "" {
			resumes++
		}
	}
	return len(remote.sends), resumes, remote.gets, remote.cancels
}

func assertNoRepeatResume(t *testing.T, runner *ShadowRunner, client *uncertainResumeClient, id string) {
	t.Helper()
	before, countBefore, _, _ := client.counts()
	for _, input := range []string{"approved", "another answer"} {
		_, err := runner.ResumeRun(t.Context(), id, input)
		if !errors.Is(err, ErrRemoteOutcomeUnknown) {
			t.Fatalf("pending physical resume was re-admitted for %q: %v", input, err)
		}
	}
	after, countAfter, _, _ := client.counts()
	if before != after || countBefore != countAfter {
		t.Fatalf("repeat ResumeRun performed external work: before=%d after=%d resumes=%d/%d", before, after, countBefore, countAfter)
	}
}

// A transport failure can happen *after* the peer accepted a resume. GET
// returning INPUT_REQUIRED is not proof that the input was never accepted.
func TestPR113ResumeOutcomeUnknownNeverResendsAcrossRestartOrSameStateGET(t *testing.T) {
	store := memory.NewStore()
	rt, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	remote := newUncertainResumeClient()
	runner := newIdentityTestShadow(t, rt, remote)
	initial, err := runner.StartRun(t.Context(), shadowStartRequest("pr113-unknown-resume"))
	if err != nil || initial.Run.Status != kernel.RunStatusWaitingInput {
		t.Fatalf("initial wait failed: %+v %v", initial.Run, err)
	}
	remote.resumeErr = errors.New("lost reply after success: Bearer private-secret-123")
	pending, err := runner.ResumeRun(t.Context(), initial.Run.ID, "approved")
	if !errors.Is(err, ErrRemoteOutcomeUnknown) || strings.Contains(err.Error(), "private-secret") ||
		pending.Run.Status != kernel.RunStatusRunning || pending.Run.ErrorDetail != "" {
		t.Fatalf("resume ambiguity leaked or was falsely made retryable: %+v %v", pending.Run, err)
	}
	persisted, err := rt.Load(t.Context(), initial.Run.ID)
	if err != nil || persisted.Run.Revision != initial.Run.Revision+2 || persisted.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("original physical send intent not durable: %+v %v", persisted.Run, err)
	}
	state, err := runner.decodeState(persisted)
	if err != nil || state.PendingMessageID != initial.Run.ID+":message:2" || state.PendingInputHash == "" || !state.PendingMessageReturned {
		t.Fatalf("lost original pending resume proof: %+v %v", state, err)
	}
	wire, err := json.Marshal(persisted)
	if err != nil || strings.Contains(string(wire), "private-secret") {
		t.Fatalf("persisted transport credential: %s %v", wire, err)
	}
	assertNoRepeatResume(t, runner, remote, initial.Run.ID)
	// Rebuild BOTH Kernel and adapter with the same Store and private proof
	// key: no process-local pointer or prior in-memory state grants a retry.
	restarted, err := kernel.New(kernel.Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	recovered := newIdentityTestShadow(t, restarted, remote)
	assertNoRepeatResume(t, recovered, remote, initial.Run.ID)
	observed, err := recovered.LoadRun(t.Context(), initial.Run.ID)
	if !errors.Is(err, ErrRemoteOutcomeUnknown) || observed.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("same-state GET falsely authorized resend: %+v %v", observed.Run, err)
	}
	sends, resumes, gets, _ := remote.counts()
	if sends != 2 || resumes != 1 || gets != 1 {
		t.Fatalf("unexpected remote side effects: sends=%d resumes=%d gets=%d", sends, resumes, gets)
	}
	// A later independently observed *terminal* remote Task may settle the
	// original pending input without another SendMessage.
	remote.setTask(completedTask())
	terminal, err := recovered.LoadRun(t.Context(), initial.Run.ID)
	if err != nil || terminal.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("terminal original remote Task did not reconcile: %+v %v", terminal.Run, err)
	}
	if _, err := recovered.decodeState(terminal); err != nil {
		t.Fatalf("terminal lost attestation: %v", err)
	}
	if _, resumes, _, _ := remote.counts(); resumes != 1 {
		t.Fatalf("terminal reconciliation resent input: %d", resumes)
	}
}

func TestPR113ResumeMalformedAcknowledgementsRemainUnresolved(t *testing.T) {
	for _, mode := range []string{"malformed", "foreign", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			rt := newShadowRuntime(t)
			remote := newUncertainResumeClient()
			runner := newIdentityTestShadow(t, rt, remote)
			initial, err := runner.StartRun(t.Context(), shadowStartRequest("pr113-resume-"+mode))
			if err != nil {
				t.Fatal(err)
			}
			remote.resumeMode = mode
			_, err = runner.ResumeRun(t.Context(), initial.Run.ID, "approved")
			if err == nil {
				t.Fatal("unverifiable SendMessage reply was accepted")
			}
			assertNoRepeatResume(t, runner, remote, initial.Run.ID)
			observed, err := rt.Load(t.Context(), initial.Run.ID)
			if err != nil || observed.Run.Status != kernel.RunStatusRunning {
				t.Fatalf("unverifiable reply changed terminal state: %+v %v", observed.Run, err)
			}
			if _, err := runner.decodeState(observed); err != nil {
				t.Fatal(err)
			}
			if _, resumes, _, _ := remote.counts(); resumes != 1 {
				t.Fatalf("physical resumes=%d", resumes)
			}
		})
	}
}

// A competing worker (or a restarted process) sees the durable send intent
// while the original physical request is still blocked. CAS grants one send.
func TestPR113ConcurrentResumeAdmissionAndRestartAreSinglePhysicalSend(t *testing.T) {
	rt := newShadowRuntime(t)
	remote := newUncertainResumeClient()
	runner := newIdentityTestShadow(t, rt, remote)
	initial, err := runner.StartRun(t.Context(), shadowStartRequest("pr113-concurrent-resume"))
	if err != nil {
		t.Fatal(err)
	}
	remote.entered = make(chan struct{}, 1)
	remote.release = make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(remote.release) }) }
	defer release()
	outcome := make(chan error, 1)
	go func() { _, err := runner.ResumeRun(t.Context(), initial.Run.ID, "approved"); outcome <- err }()
	select {
	case <-remote.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first physical resume never entered")
	}
	assertNoRepeatResume(t, runner, remote, initial.Run.ID)
	reconstructed := newIdentityTestShadow(t, rt, remote)
	assertNoRepeatResume(t, reconstructed, remote, initial.Run.ID)
	// The original sender can still commit its verified response.
	remote.setTask(completedTask())
	release()
	select {
	case err := <-outcome:
		if err != nil {
			t.Fatalf("original sender failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("original sender did not finish")
	}
	last, err := reconstructed.LoadRun(t.Context(), initial.Run.ID)
	if err != nil || last.Run.Status != kernel.RunStatusCompleted {
		t.Fatalf("original result lost: %+v %v", last.Run, err)
	}
	if sends, resumes, _, _ := remote.counts(); sends != 2 || resumes != 1 {
		t.Fatalf("concurrent/restarted process duplicated remote send: sends=%d resumes=%d", sends, resumes)
	}
}

func TestPR113UncertainResumeCanCancelOriginalBoundTask(t *testing.T) {
	rt := newShadowRuntime(t)
	remote := newUncertainResumeClient()
	runner := newIdentityTestShadow(t, rt, remote)
	initial, err := runner.StartRun(t.Context(), shadowStartRequest("pr113-uncertain-cancel"))
	if err != nil {
		t.Fatal(err)
	}
	remote.resumeErr = errors.New("transport reply lost")
	if _, err := runner.ResumeRun(t.Context(), initial.Run.ID, "approved"); !errors.Is(err, ErrRemoteOutcomeUnknown) {
		t.Fatalf("uncertain input accepted: %v", err)
	}
	cancelled, err := runner.CancelRun(t.Context(), initial.Run.ID)
	if err != nil || cancelled.Run.Status != kernel.RunStatusCancelled {
		t.Fatalf("known original Task was not cancellable: %+v %v", cancelled.Run, err)
	}
	if _, err := runner.decodeState(cancelled); err != nil {
		t.Fatalf("cancel attestation missing: %v", err)
	}
	if sends, resumes, gets, cancels := remote.counts(); sends != 2 || resumes != 1 || gets != 1 || cancels != 1 {
		t.Fatalf("cancellation side-effect counts unexpected: %d/%d/%d/%d", sends, resumes, gets, cancels)
	}
}

// Even when a verified GET reports a different nonterminal Task state,
// neither a fresh wait nor an unknown send receipt grants another input.
func TestPR113NonterminalGETCannotReopenUncertainResume(t *testing.T) {
	for _, task := range []TaskSnapshot{inputRequiredTask(), workingTask(), authRequiredTask()} {
		t.Run(task.State, func(t *testing.T) {
			rt := newShadowRuntime(t)
			remote := newUncertainResumeClient()
			runner := newIdentityTestShadow(t, rt, remote)
			initial, err := runner.StartRun(t.Context(), shadowStartRequest("pr113-nonterminal-"+task.State))
			if err != nil {
				t.Fatal(err)
			}
			remote.resumeErr = errors.New("unknown effect")
			pending, err := runner.ResumeRun(t.Context(), initial.Run.ID, "approved")
			if !errors.Is(err, ErrRemoteOutcomeUnknown) {
				t.Fatal(err)
			}
			remote.setTask(task)
			observed, err := runner.LoadRun(t.Context(), initial.Run.ID)
			if !errors.Is(err, ErrRemoteOutcomeUnknown) || observed.Run.Revision != pending.Run.Revision ||
				observed.Run.Status != kernel.RunStatusRunning {
				t.Fatalf("nonterminal GET reopened input: %+v %v", observed.Run, err)
			}
			assertNoRepeatResume(t, runner, remote, initial.Run.ID)
		})
	}
}

func TestPR113PublicResumeControlExposesOriginalPendingOutcome(t *testing.T) {
	rt := newShadowRuntime(t)
	remote := newUncertainResumeClient()
	plugin, err := NewPlugin(PluginDependencies{Runtime: rt, AttestationKey: testA2AShadowKey(),
		Bindings: BindingResolverFunc(func(_ context.Context, target, rev string) (Binding, error) {
			return Binding{TargetID: target, Revision: rev, Client: remote,
				Discovery: Discovery{Descriptor: RemoteAgentDescriptor{
					Name: "remote-agent", PreferredURL: testShadowRemoteURL, ProtocolVersion: ProtocolVersion,
				}}}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	delegation := handoff.Delegation{ID: "delegate", MemberID: "a2a:remote", MemberRevision: "revision-1",
		ChildRunID: "pr113-control-resume", Goal: "remote goal", Status: handoff.StatusQueued}
	child, err := plugin.ResolveChild(t.Context(), delegation)
	if err != nil {
		t.Fatal(err)
	}
	runner := child.(*ShadowRunner)
	initial, err := runner.StartRun(t.Context(), shadowStartRequest(delegation.ChildRunID))
	if err != nil {
		t.Fatal(err)
	}
	remote.resumeErr = errors.New("Bearer private credential")
	pending, err := plugin.ResumeRun(t.Context(), initial.Run.ID, initial.Run.Revision, "approved")
	if !errors.Is(err, ErrRemoteOutcomeUnknown) || strings.Contains(err.Error(), "private") ||
		pending.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("public Control leaked or lost uncertainty: %+v %v", pending.Run, err)
	}
	_, err = plugin.ResumeRun(t.Context(), pending.Run.ID, pending.Run.Revision, "approved")
	if !errors.Is(err, ErrRemoteOutcomeUnknown) {
		t.Fatalf("public Control misclassified unresolved original input: %v", err)
	}
	if _, resumes, _, _ := remote.counts(); resumes != 1 {
		t.Fatalf("public Control re-sent %d inputs", resumes)
	}
}
