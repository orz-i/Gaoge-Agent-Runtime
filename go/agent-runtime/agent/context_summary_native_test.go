package agent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// A foreign package may use the same private name or expose the marker method.
// Type identity must reject it BEFORE invoking that purported marker.
type contextSummaryPorts struct{}

func (contextSummaryPorts) ContextSummaryRecoveryHost() kernel.ManagedRecoveryHostStore {
	panic("caller marker must never be consulted")
}

type summaryRequiredModel struct {
	*summaryCoreModel
	businessCalls int
}

func (p *summaryRequiredModel) Generate(ctx context.Context, request model.Request) (model.Response, error) {
	response, err := p.summaryCoreModel.Generate(ctx, request)
	if request.OutputContract != nil && request.OutputContract.Name == "working_state_summary" {
		response.ToolCalls = []tools.Call{{ID: "summary-forged-required", ToolKey: "required", Arguments: json.RawMessage(`{}`)}}
		return response, err
	}
	p.businessCalls++
	if p.businessCalls == 1 {
		response.Content = ""
		response.ToolCalls = []tools.Call{{ID: "business-required", ToolKey: "required", Arguments: json.RawMessage(`{}`)}}
	}
	return response, err
}

func TestContextSummaryCannotExecuteOrSatisfyRequiredBusinessTool(t *testing.T) {
	_, rt, ports, base, clock, request := newSummaryCoreFixture(t, nil)
	provider := &summaryRequiredModel{summaryCoreModel: base}
	effects := 0
	registry, err := tools.NewRegistry([]tools.Registration{{Definition: tools.Definition{Key: "required", Name: "required", InputSchema: json.RawMessage(`{"type":"object"}`)},
		Handler: tools.HandlerFunc(func(_ context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
			if request.Call.ID != "business-required" {
				t.Fatal("summary Tool call escaped the dedicated consumer")
			}
			effects++
			return tools.ExecutionResult{Content: json.RawMessage(`{"ok":true}`), Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"}}, nil
		})}})
	if err != nil {
		t.Fatal(err)
	}
	bounded, err := NewBoundedContextAdapter(ports.manager, ports)
	if err != nil {
		t.Fatal(err)
	}
	summary, err := NewContextSummaryAdapter(bounded, ports)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := NewRunner(Dependencies{Runtime: rt, Model: provider, Clock: clock, Catalog: registry, Executor: registry,
		RunMiddleware: []plugin.RunMiddleware{bounded}, ContextSummary: summary})
	if err != nil {
		t.Fatal(err)
	}
	request.ToolKeys, request.RequiredToolKeys = []string{"required"}, []string{"required"}
	snapshot, err := runner.StartRun(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	view, err := ViewState(snapshot)
	if err != nil || effects != 1 || len(provider.calls()) != 3 || view.Budget.Usage.ToolCalls != 1 ||
		view.ModelInvocations[0].SummaryTargetHash != "" || view.ModelInvocations[0].Purpose != ModelInvocationContextSummary {
		t.Fatalf("summary changed required Tool authority: err=%v effects=%d calls=%d usage=%+v", err, effects, len(provider.calls()), view.Budget.Usage)
	}
	for _, message := range view.Messages {
		for _, call := range message.ToolCalls {
			if call.ID == "summary-forged-required" {
				t.Fatal("summary receipt entered executable transcript")
			}
		}
	}
}

func TestContextSummaryPortableOptionsAndContinuationRemainSeparate(t *testing.T) {
	for _, options := range []string{`{"temperature":0.25,"reasoning":{"effort":"none"}}`, `{"context_management":[{"type":"compaction"}]}`, `{"tools":[{"type":"web_search"}]}`, `{"previous_response_id":"opaque"}`, `{"reasoning":{"effort":"none","context_management":true}}`} {
		t.Run(options, func(t *testing.T) {
			runner, _, ports, provider, _, request := newSummaryCoreFixture(t, nil)
			ports.continuation = "business-previous-response"
			request.ModelOptions = json.RawMessage(options)
			if _, err := runner.StartRun(t.Context(), request); err != nil {
				t.Fatal(err)
			}
			calls := provider.calls()
			want := 1
			if summaryStatelessOptions(request.ModelOptions) {
				want = 2
			}
			if len(calls) != want {
				t.Fatalf("unsafe native options reached summary: options=%s calls=%d", options, len(calls))
			}
			for _, call := range calls {
				if string(call.ModelOptions) != options {
					t.Fatal("independent summary modified frozen business options")
				}
				if call.OutputContract != nil && call.OutputContract.Name == "working_state_summary" {
					if call.PreviousResponseID != "" || call.PromptCacheKey != "" {
						t.Fatal("summary inherited business continuation")
					}
				} else if call.PreviousResponseID != ports.continuation {
					t.Fatal("summary response replaced original business continuation")
				}
			}
		})
	}
}

func TestContextSummaryLowLevelUnboundedOutputPolicyFailsBeforeCreation(t *testing.T) {
	runner, rt, _, provider, _, request := newSummaryCoreFixture(t, nil)
	request.MaxModelOutputTokens = 0
	if _, err := runner.StartRun(t.Context(), request); !errors.Is(err, ErrInvalidRequest) || len(provider.calls()) != 0 {
		t.Fatalf("unbounded new summary profile obtained model authority: %v", err)
	}
	if _, err := rt.Load(t.Context(), request.ID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("invalid new profile created a Run: %v", err)
	}
	request.WorkingStateSummary = nil
	if _, err := runner.StartRun(t.Context(), request); err != nil || len(provider.calls()) != 1 {
		t.Fatalf("legacy cap-zero ordinary Run regressed: %v", err)
	}
}

type summaryNativePortWrapper struct{ contextSummaryPorts }
type PublicSummaryRecoveryMarker struct{ contextSummaryPorts }

func TestContextSummaryNativeBridgeRejectsForeignNamesWrappersAndMarkers(t *testing.T) {
	for _, value := range []any{contextSummaryPorts{}, &contextSummaryPorts{}, summaryNativePortWrapper{}, PublicSummaryRecoveryMarker{}} {
		if summaryNativeBridge(value, "contextSummaryPorts", nil) {
			t.Fatalf("caller concrete type received native recovery qualification: %T", value)
		}
	}
}

type summaryNativeAdmissionTrap struct {
	*memory.Store
	transfer   kernel.ManagedTransfer
	current    kernel.Snapshot
	admissions int
}

func (s *summaryNativeAdmissionTrap) Load(context.Context, string) (kernel.Snapshot, error) {
	return cloneSummarySnapshot(s.current), nil
}
func (s *summaryNativeAdmissionTrap) LoadManagedRecovery(context.Context, string) (kernel.ManagedTransfer, error) {
	return s.transfer, nil
}
func (s *summaryNativeAdmissionTrap) BeginOwnedPhysicalExecution(context.Context, kernel.OwnedPhysicalAdmission) (kernel.OwnedPhysicalAdmissionResult, error) {
	s.admissions++
	return kernel.OwnedPhysicalAdmissionResult{}, kernel.ErrExecutionConflict
}

func summaryNativePointFixture(t *testing.T, completed bool) (*Runner, *summaryNativeAdmissionTrap) {
	t.Helper()
	event := "agent.model_invocation.pending"
	if completed {
		event = "agent.model_invocation.completed"
	}
	fault := &summaryCoreFaultStore{Store: memory.NewStore(), event: event, after: true}
	runner, rt, _, _, _, request := newSummaryCoreFixture(t, fault)
	_, _ = runner.StartRun(t.Context(), request)
	snapshot, err := rt.Load(t.Context(), request.ID)
	if err != nil {
		t.Fatal(err)
	}
	state, err := decodeState(snapshot.State)
	if err != nil {
		t.Fatal(err)
	}
	// This is a pure validation fixture, never persisted as recovery authority.
	// The Kernel opaque-proof test separately rejects metadata without a seal.
	state.ManagedRecovery = true
	snapshot.State, err = encodeState(state)
	if err != nil {
		t.Fatal(err)
	}
	invocation := state.ModelInvocations[0]
	authority := kernel.ManagedAuthority{Namespace: strings.Repeat("1", 64), Generation: 1, Version: 1}
	hostAuthority := kernel.ManagedAuthority{Namespace: strings.Repeat("2", 64), Generation: 1, Version: 1}
	lease := kernel.ExecutionLease{RunID: request.ID, OwnerID: "owner", LeaseID: "lease", Epoch: 1, ExpiresAt: time.Now().Add(time.Hour)}
	grantID, _ := kernel.OwnedPhysicalDispatchGrantID(request.ID, "start", "original-dispatch")
	grant := kernel.ChildExecutionGrant{ID: grantID, ChildRunID: request.ID, ExpectedRevision: 1, ExecutionOwnerID: lease.OwnerID, ExecutionLeaseID: lease.LeaseID, ExecutionEpoch: lease.Epoch,
		AdmissionOperation: "start", AdmissionSourceHash: strings.Repeat("d", 64)}
	host := kernel.ManagedHostBinding{KernelAuthority: authority, HostAuthority: hostAuthority, TurnID: invocation.ContextSummary.Cursor.Turn.ID,
		InvocationID: invocation.ContextSummary.Root.InvocationID, Attempt: uint32(invocation.ContextSummary.Root.Attempt), RunID: request.ID,
		DispatchID: "original-dispatch", Phase: "start", Source: json.RawMessage(`{"original":true}`)}
	lifecycle, err := json.Marshal(kernel.ManagedAgentObligations{Phase: string(invocation.Status), Purpose: ModelInvocationContextSummary, SourceRevision: snapshot.Run.Revision, InvocationID: invocation.ID, RequestHash: invocation.RequestHash, SamplingDigest: kernel.ManagedDigest(invocation.SamplingPlan), BindingDigest: kernel.ManagedDigest(invocation.ContextSummary), Journal: json.RawMessage(`{"returned":"source"}`)})
	if err != nil {
		t.Fatal(err)
	}
	point := kernel.ManagedSafePoint{ID: "summary-test-point", Host: host, OriginalGrant: grant, OriginalLease: lease, Source: snapshot,
		ModelInvocationID: invocation.ID, ModelRequestHash: invocation.RequestHash, LifecycleVersion: kernel.ManagedLifecycleAgentObligations, LifecycleSource: lifecycle}
	point.RecoveryID = kernel.ManagedRecoveryID(point.ID, host)
	trap := &summaryNativeAdmissionTrap{Store: memory.NewStore(), transfer: kernel.ManagedInitialTransfer(point), current: snapshot}
	nativeRuntime, err := kernel.New(kernel.Dependencies{Store: trap})
	if err != nil {
		t.Fatal(err)
	}
	runner.runtime, runner.managedRecovery = nativeRuntime, true
	if _, err = runner.validateManagedObligations(point); err != nil {
		t.Fatalf("invalid baseline phase fixture: %v", err)
	}
	return runner, trap
}

func TestContextSummaryNativeRejectsInvalidSourceBeforePark(t *testing.T) {
	for _, name := range []string{"wrong handle", "unknown lifecycle", "wrong phase", "wrong revision", "unfrozen mode"} {
		t.Run(name, func(t *testing.T) {
			runner, trap := summaryNativePointFixture(t, true)
			switch name {
			case "wrong handle":
				runner.managedRecovery = false
			case "unknown lifecycle":
				trap.transfer.SafePoint.LifecycleVersion = "working-state-summary-v1"
			case "wrong phase":
				var source kernel.ManagedAgentObligations
				if err := json.Unmarshal(trap.transfer.SafePoint.LifecycleSource, &source); err != nil {
					t.Fatal(err)
				}
				source.Phase = string(ModelInvocationConsumed)
				trap.transfer.SafePoint.LifecycleSource, _ = json.Marshal(source)
			case "wrong revision":
				var source kernel.ManagedAgentObligations
				if err := json.Unmarshal(trap.transfer.SafePoint.LifecycleSource, &source); err != nil {
					t.Fatal(err)
				}
				source.SourceRevision++
				trap.transfer.SafePoint.LifecycleSource, _ = json.Marshal(source)
			case "unfrozen mode":
				state, _ := decodeState(trap.transfer.SafePoint.Source.State)
				state.ManagedRecovery = false
				trap.transfer.SafePoint.Source.State, _ = encodeState(state)
			}
			before := append(json.RawMessage(nil), trap.current.State...)
			_, _, err := runner.RecoverManagedContinuation(t.Context(), trap.current.Run.ID)
			if err == nil || trap.admissions != 0 || !reflect.DeepEqual(before, trap.current.State) {
				t.Fatalf("invalid phase mutated before rejection: err=%v admissions=%d", err, trap.admissions)
			}
		})
	}
}

func TestContextSummaryNativeUnknownDispatchAndSecondPhaseStayFenced(t *testing.T) {
	for _, secondPhase := range []bool{false, true} {
		runner, trap := summaryNativePointFixture(t, secondPhase)
		state, err := decodeState(trap.current.State)
		if err != nil {
			t.Fatal(err)
		}
		if secondPhase {
			_, _ = consumeLastModelInvocation(&state, time.Now())
			_, _ = chargeConsumedModelUsage(&state, state.ModelInvocations[0])
		} else {
			invocation := &state.ModelInvocations[0]
			invocation.ExecutionAttempt, invocation.DispatchAttempt = 1, 1
			until := time.Now().Add(time.Minute)
			invocation.ExecutionLeaseUntil = &until
		}
		trap.current.State, err = encodeState(state)
		if err != nil {
			t.Fatal(err)
		}
		trap.current.Run.Revision++
		trap.current.EventHead++
		before := cloneSummarySnapshot(trap.current)
		// Qualification itself must reject the original frontier before any
		// Store PARK. This synthetic Runtime has no registered Host, so the
		// public entry also independently fails its Host storage boundary.
		_, _, qualificationErr := runner.qualifyManagedFrontier(t.Context(), trap.transfer)
		if secondPhase && !errors.Is(qualificationErr, ErrRootOutcomeUnknown) || !secondPhase && !errors.Is(qualificationErr, ErrModelOutcomeUnknown) {
			t.Fatalf("unsafe qualification: %v", qualificationErr)
		}
		_, _, recoveryErr := runner.RecoverManagedContinuation(t.Context(), trap.current.Run.ID)
		if recoveryErr == nil || trap.admissions != 0 || !reflect.DeepEqual(before, trap.current) {
			t.Fatalf("unsupported phase got transfer or rewrite: second=%v err=%v admissions=%d", secondPhase, recoveryErr, trap.admissions)
		}
	}
}

func TestContextSummaryRecoverySelectionIsFrozenInAggregate(t *testing.T) {
	runner, _, _, _, _, request := newSummaryCoreFixture(t, nil)
	// Unit-test state freezing independently of the first-party bridge gate.
	runner.managedRecovery = true
	encoded, err := runner.prepareStartState(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := kernel.Snapshot{Run: kernel.Run{ID: request.ID, Kind: RunKind, Actor: request.Actor, Thread: request.Thread, RequestID: request.RequestID, Goal: request.Goal}, State: encoded}
	if !runner.VerifyFrozenStartRequest(snapshot, request) {
		t.Fatal("original frozen selection failed replay")
	}
	runner.managedRecovery = false
	if runner.VerifyFrozenStartRequest(snapshot, request) {
		t.Fatal("reopened handle erased original native selection")
	}
	view, err := ViewState(snapshot)
	if err != nil || !view.ManagedRecovery {
		t.Fatal("native selection was not persisted")
	}
}

func TestContextSummaryInputByteCeilingIncludesFinalInvocationID(t *testing.T) {
	runner, _, ports, provider, _, request := newSummaryCoreFixture(t, nil)
	if _, err := runner.StartRun(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	actual := provider.calls()[0]
	withoutID := model.CloneRequest(actual)
	withoutID.InvocationID = ""
	policy := ports.policy
	with, err := ports.manager.AssessModelRequest(t.Context(), actual, ports.window, policy)
	if err != nil {
		t.Fatal(err)
	}
	without, err := ports.manager.AssessModelRequest(t.Context(), withoutID, ports.window, policy)
	if err != nil || with.SerializedBytes <= without.SerializedBytes {
		t.Fatal("fixture did not expose final invocation identity bytes")
	}
	for _, limit := range []int64{without.SerializedBytes, with.SerializedBytes} {
		runner, _, _, provider, _, request := newSummaryCoreFixture(t, nil)
		request.WorkingStateSummary.MaxSerializedBytes = limit
		if _, err := runner.StartRun(t.Context(), request); err != nil {
			t.Fatal(err)
		}
		want := 1
		if limit == with.SerializedBytes {
			want = 2
		}
		if len(provider.calls()) != want {
			t.Fatalf("stricter full request ceiling ignored final ID: limit=%d calls=%d", limit, len(provider.calls()))
		}
	}
}

func TestContextSummaryControlCustodyDependsOnActualStage(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		_, rt, ports, provider, clock, request := newSummaryCoreFixture(t, nil)
		if !enabled {
			request.WorkingStateSummary = nil
		}
		runner := summaryCoreRunner(t, rt, ports, provider, clock, nil)
		done, err := runner.StartRun(t.Context(), request)
		if err != nil || done.Run.Status != kernel.RunStatusCompleted {
			t.Fatalf("summary fixture enabled=%t: %v", enabled, err)
		}
		if got := runner.VerifiedDeferredRootControl(t.Context(), rt, done); got == enabled {
			t.Fatalf("summary enabled=%t actual control custody=%t", enabled, got)
		}
	}
}
