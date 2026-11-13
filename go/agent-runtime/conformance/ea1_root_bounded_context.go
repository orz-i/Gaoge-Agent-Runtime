package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

// This is a hydration-layer vector in the existing Root Start P0/P1 suite.
// It does not claim that a computed checkpoint proves archive persistence.
func ea1RootBoundedContext(t *testing.T, factory func(*testing.T) (kernel.Store, kernel.Store), automatic bool) {
	for _, mode := range []string{"bounded", "opaque", "foreign-hydration"} {
		t.Run("P1/callback-completion-"+mode, func(t *testing.T) {
			backing, peer := factory(t)
			injected := errors.New("original pending intent CAS denied before commit")
			fault := &ea1AgentReceiptFault{Store: backing, ExecutionStore: backing.(kernel.ExecutionStore), ControlStore: backing.(kernel.ControlStore), ChildExecutionStore: backing.(kernel.ChildExecutionStore), ExecutionQuarantineStore: backing.(kernel.ExecutionQuarantineStore), OwnedPhysicalExecutionStore: backing.(kernel.OwnedPhysicalExecutionStore), event: "agent.model_invocation.pending", injected: injected}
			rt := ea1StartRuntime(t, fault)
			observer := ea1StartRuntime(t, peer)
			request := ea1AdmissionRequest()
			ports := &ea1BoundedContextPorts{runtime: observer, request: request, foreign: mode == "foreign-hydration"}
			bounded, err := agent.NewBoundedContextAdapter(runtimecontext.NewManager(runtimecontext.Dependencies{}), ports)
			if err != nil {
				t.Fatal(err)
			}
			var middleware plugin.RunMiddleware = bounded
			if mode == "opaque" {
				middleware = &ea1AdmissionMiddleware{}
			}
			physical := &ea1AdmissionModel{}
			runner := ea1AdmissionConfiguredRunner(t, rt, physical, middleware, automatic)
			_, firstErr := runner.StartRun(t.Context(), request)
			if firstErr == nil || physical.calls.Load() != 0 {
				t.Fatalf("denied callback sent or succeeded: calls=%d err=%v", physical.calls.Load(), firstErr)
			}
			original, err := observer.Load(t.Context(), request.ID)
			if err != nil || !runner.VerifyFrozenStartRequest(original, request) {
				t.Fatalf("original source lost: %v", err)
			}
			if ports.unexpected != 0 {
				t.Fatal("hydration vector performed archival or preparation operations")
			}
			if mode != "foreign-hydration" && (!errors.Is(firstErr, injected) || fault.failures != 1) {
				t.Fatalf("actual pre-intent CAS not reached: %v", firstErr)
			}
			if mode == "bounded" {
				if ports.hydrates != 1 || !reflect.DeepEqual(original, ports.before) {
					t.Fatal("bounded completion mutated original failed-CAS snapshot")
				}
				if _, err = observer.LookupChildExecution(t.Context(), request.ID); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatalf("actual bounded completion did not retire original scope: %v", err)
				}
				done, resumeErr := runner.Resume(t.Context(), request.ID, original.Run.Revision)
				if resumeErr != nil || done.Run.Status != kernel.RunStatusCompleted || physical.calls.Load() != 1 || ports.hydrates != 2 {
					t.Fatalf("fresh admitted recovery failed or duplicated: calls=%d hydration=%d err=%v", physical.calls.Load(), ports.hydrates, resumeErr)
				}
				if !runner.VerifyFrozenStartRequest(done, request) {
					t.Fatal("bounded recovery changed frozen original request")
				}
				if _, err = observer.LookupChildExecution(t.Context(), request.ID); !errors.Is(err, kernel.ErrNotFound) {
					t.Fatalf("completed bounded recovery retained physical grant: %v", err)
				}
				return
			}
			grant, err := observer.LookupChildExecution(t.Context(), request.ID)
			if err != nil || grant.ChildRunID != request.ID {
				t.Fatalf("unproved callback lost original scope: %v", err)
			}
			_, resumeErr := runner.Resume(t.Context(), request.ID, original.Run.Revision)
			if !errors.Is(resumeErr, agent.ErrRootOutcomeUnknown) || physical.calls.Load() != 0 {
				t.Fatalf("unproved callback recovered by assertion: %v", resumeErr)
			}
			same, err := observer.LookupChildExecution(t.Context(), request.ID)
			if err != nil || same != grant {
				t.Fatal("rejected recovery changed original grant")
			}
			ea1CompBarrierUnchanged(t, observer, original)
			if mode == "foreign-hydration" && (ports.hydrates != 1 || fault.failures != 0) {
				t.Fatal("foreign hydration reached original Next")
			}
		})
	}
}

type ea1BoundedContextPorts struct {
	runtime              *kernel.Runtime
	request              agent.StartRequest
	before               kernel.Snapshot
	hydrates, unexpected int
	foreign              bool
}

func (p *ea1BoundedContextPorts) Hydrate(ctx context.Context, inv plugin.RunInvocation) (context.Context, agent.BoundedContextHydration, error) {
	p.hydrates++
	snapshot, err := p.runtime.Load(ctx, inv.RunID)
	if err != nil {
		return ctx, agent.BoundedContextHydration{}, err
	}
	if inv.RunID != p.request.ID || snapshot.Run.Actor != p.request.Actor || snapshot.Run.Thread != p.request.Thread || inv.Actor != snapshot.Run.Actor || inv.Thread != snapshot.Run.Thread {
		return ctx, agent.BoundedContextHydration{}, kernel.ErrInvalidInput
	}
	p.before = snapshot
	source, err := json.Marshal(struct {
		RunID  string
		Actor  kernel.ActorRef
		Thread kernel.ThreadRef
		Goal   string
	}{snapshot.Run.ID, snapshot.Run.Actor, snapshot.Run.Thread, snapshot.Run.Goal})
	id := snapshot.Run.ID
	if p.foreign {
		id = "foreign-hydration-source"
	}
	return ctx, agent.BoundedContextHydration{RunID: id, SourceBinding: source}, err
}
func (p *ea1BoundedContextPorts) Plan(context.Context, model.Request) (agent.BoundedContextPlan, error) {
	p.unexpected++
	return agent.BoundedContextPlan{}, errors.New("unexpected preparation")
}
func (p *ea1BoundedContextPorts) PutArtifact(context.Context, runtimecontext.Artifact) (runtimecontext.Artifact, error) {
	p.unexpected++
	return runtimecontext.Artifact{}, errors.New("unexpected archive write")
}
func (p *ea1BoundedContextPorts) CommitCheckpoint(context.Context, agent.BoundedContextPlan, runtimecontext.Checkpoint) (runtimecontext.Checkpoint, error) {
	p.unexpected++
	return runtimecontext.Checkpoint{}, errors.New("unexpected checkpoint CAS")
}
func (p *ea1BoundedContextPorts) BindCheckpoint(context.Context, string, runtimecontext.Checkpoint) error {
	p.unexpected++
	return errors.New("unexpected checkpoint binding")
}
