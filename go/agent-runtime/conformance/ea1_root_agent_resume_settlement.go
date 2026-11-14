package conformance

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

// RunEA1RootAgentResumeSettlementSuite covers AR-R P3/P4 through actual Resume
// of a PrepareRun-created Root. Root has no ancestor. Memory retains state;
// SQLite reopens and real PostgreSQL uses an independent connection pool.
func RunEA1RootAgentResumeSettlementSuite(t *testing.T, factory func(*testing.T) (kernel.Store, func() kernel.Store)) {
	for _, phase := range []string{"P3", "P4"} {
		for _, tool := range []bool{false, true} {
			kind := map[bool]string{false: "model-yield", true: "terminal-tool"}[tool]
			t.Run(phase+"/"+kind, func(t *testing.T) { ea1RootResumeSettlement(t, factory, phase, tool, false) })
		}
	}
	t.Run("P3/committed-model-receipt-unknown-host", func(t *testing.T) { ea1RootResumeSettlement(t, factory, "P3", false, true) })
}

// Keep the Start middleware contract unchanged; this adapter admits only Resume.
type ea1ResumeSettlementMiddleware struct{ physical *ea1StartSettlementPhysical }

func (ea1ResumeSettlementMiddleware) Name() string { return "ea1.original-resume-callback" }
func (m ea1ResumeSettlementMiddleware) Run(ctx context.Context, inv plugin.RunInvocation, next plugin.RunNext) (kernel.Snapshot, error) {
	p := m.physical
	p.mu.Lock()
	p.hostCalls++
	p.mu.Unlock()
	if inv.Operation != plugin.RunResume || inv.ParentRunID != "" {
		return kernel.Snapshot{}, errors.New("not original Root Resume callback")
	}
	result, err := next(ctx)
	if p.hostUnknown {
		p.mu.Lock()
		p.hostSends++
		p.mu.Unlock()
		return result, errors.Join(err, p.hostFault)
	}
	return result, err
}

func ea1RootResumeSettlement(t *testing.T, factory func(*testing.T) (kernel.Store, func() kernel.Store), phase string, withTool, unknownHost bool) {
	t.Helper()
	backing, reopen := factory(t)
	fault := errors.New("test-only original settlement transport fault")
	mode := phase
	if unknownHost {
		mode = "unknown"
	}
	wrapped := &ea1StartSettlementStore{Store: backing, ExecutionStore: backing.(kernel.ExecutionStore), ControlStore: backing.(kernel.ControlStore), ChildExecutionStore: backing.(kernel.ChildExecutionStore), OwnedPhysicalExecutionStore: backing.(kernel.OwnedPhysicalExecutionStore), mode: "setup", fault: fault}
	rt, err := kernel.New(kernel.Dependencies{Store: wrapped})
	if err != nil {
		t.Fatal(err)
	}
	physical := &ea1StartSettlementPhysical{rt: rt, tool: withTool, hostUnknown: unknownHost, hostFault: errors.New("original Host send outcome unknown")}
	definition := tools.Definition{Key: "effect", Name: "Original Effect", Description: "frozen terminal effect", InputSchema: json.RawMessage(`{"type":"object","properties":{"source":{"type":"string"}},"required":["source"]}`), Terminal: true}
	catalog, err := tools.NewRegistry([]tools.Registration{{Definition: definition, Handler: tools.HandlerFunc(physical.execute)}})
	if err != nil {
		t.Fatal(err)
	}
	makeRunner := func(runtime *kernel.Runtime, owner string) *agent.Runner {
		r, e := agent.NewRunner(agent.Dependencies{Runtime: runtime, Model: physical, Catalog: catalog, Executor: catalog, RunMiddleware: []plugin.RunMiddleware{ea1ResumeSettlementMiddleware{physical}}, ToolMiddleware: []plugin.ToolMiddleware{physical}, RunExecution: &agent.RunExecution{OwnerID: owner, LeaseDuration: 3 * time.Minute}})
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	setup, err := agent.NewRunner(agent.Dependencies{Runtime: rt, Model: physical, Catalog: catalog, Executor: catalog, RunExecution: &agent.RunExecution{OwnerID: "prepare-only-owner", LeaseDuration: 3 * time.Minute}})
	if err != nil {
		t.Fatal(err)
	}
	runner := makeRunner(rt, "original-resume-owner")
	req := agent.StartRequest{ID: "root-resume-settlement", RequestID: "original-request", Actor: kernel.ActorRef{TenantID: "tenant", ActorID: "owner"}, Thread: kernel.ThreadRef{Kind: "conversation", ID: "original-thread"}, Goal: "original root settlement", Instructions: "Keep the original source.", Model: "fixture-model", ModelOptions: json.RawMessage(`{"temperature":0.1}`), Limits: agent.Limits{MaxLLMCalls: 2, MaxToolCalls: 1}}
	if withTool {
		req.ToolKeys = []string{"effect"}
	}
	var created kernel.Snapshot
	wrapped.created = func(s kernel.Snapshot) {
		created = s
		if s.Run.ParentRunID != "" || req.OriginFingerprint != "" || !setup.VerifyFrozenStartRequest(s, req) {
			t.Fatal("actual Create did not preserve original Root source")
		}
	}
	prepared, err := setup.PrepareRun(t.Context(), req)
	if err != nil || created.Run.ID != req.ID || prepared.Run.Status != kernel.RunStatusRunning {
		t.Fatalf("actual PrepareRun did not create resumable source: %v", err)
	}
	if _, err = rt.LookupChildExecution(t.Context(), req.ID); !errors.Is(err, kernel.ErrNotFound) {
		t.Fatalf("setup scope not retired before Resume: %v", err)
	}
	physical.mu.Lock()
	setupDispatched := len(physical.requests) != 0 || len(physical.responses) != 0 || len(physical.toolRequests) != 0 || physical.hostCalls != 0
	physical.mu.Unlock()
	if setupDispatched {
		t.Fatal("PrepareRun dispatched physical work")
	}
	// Arm only after real deferred creation and its setup scope retirement.
	wrapped.mode = mode
	wrapped.markCalls, wrapped.markCommits, wrapped.finishCalls = 0, 0, 0
	_, startErr := runner.Resume(t.Context(), req.ID, prepared.Run.Revision)
	if unknownHost {
		if !errors.Is(startErr, physical.hostFault) || wrapped.markCalls != 0 || wrapped.finishCalls != 0 {
			t.Fatalf("unknown Host retired after model receipt: %v", startErr)
		}
	} else if !errors.Is(startErr, fault) || !errors.Is(startErr, agent.ErrRootOutcomeUnknown) {
		t.Fatalf("cutpoint not retained: %v", startErr)
	}
	before, err := rt.Load(t.Context(), req.ID)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := rt.LookupChildExecution(t.Context(), req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if grant.ExecutionOwnerID != "original-resume-owner" || grant.ExecutionLeaseID == "" || grant.ExecutionEpoch == 0 || grant.AdmissionOperation != "resume" || len(grant.AdmissionSourceHash) != 64 || grant.ExpectedRevision != prepared.Run.Revision {
		t.Fatal("original owned source binding missing")
	}
	if phase == "P4" && !unknownHost {
		if wrapped.markCalls != 2 || wrapped.markCommits != 2 || wrapped.finishCalls != 1 {
			t.Fatal("actual original Mark commit/ACK-loss not reached")
		}
	} else if !unknownHost && (wrapped.markCommits != 0 || wrapped.markCalls < 2) {
		t.Fatal("P3 did not retain unmarked callback-return fact")
	}
	completion := ea1AssertOriginalRootCompletion(t, rt, grant, before, !unknownHost, true)
	ea1AssertStartSettlementReceipt(t, physical, req, definition, before, withTool)
	freshStore := reopen()
	freshRT, e := kernel.New(kernel.Dependencies{Store: freshStore})
	if e != nil {
		t.Fatal(e)
	}
	fresh := makeRunner(freshRT, "fresh-recovery-owner")
	assertUnchanged := func() {
		t.Helper()
		snap, e := freshRT.Load(t.Context(), req.ID)
		if e != nil || !reflect.DeepEqual(snap, before) {
			t.Fatal("recovery/denial changed full original snapshot")
		}
		ea1AssertStartSettlementReceipt(t, physical, req, definition, snap, withTool)
	}
	original, e := freshRT.LookupChildExecution(t.Context(), req.ID)
	if e != nil || original != grant {
		t.Fatal("reopen lost original immutable scope")
	}
	assertUnchanged()
	// Exact identity remains required even if the caller knows a real marker.
	for _, mutate := range []func(*kernel.ChildExecutionGrant){func(g *kernel.ChildExecutionGrant) { g.ID += "-foreign" }, func(g *kernel.ChildExecutionGrant) { g.ExecutionEpoch++ }, func(g *kernel.ChildExecutionGrant) { g.AdmissionOperation = "start" }, func(g *kernel.ChildExecutionGrant) { g.AdmissionSourceHash = strings.Repeat("0", 64) }} {
		wrong := grant
		mutate(&wrong)
		if e = freshStore.(kernel.ChildExecutionStore).FinishChildExecution(t.Context(), wrong); !errors.Is(e, kernel.ErrExecutionConflict) {
			t.Fatalf("foreign authority retired original: %v", e)
		}
		same, e := freshRT.LookupChildExecution(t.Context(), req.ID)
		if e != nil || same != grant {
			t.Fatal("denied identity changed grant")
		}
		assertUnchanged()
	}
	if phase == "P3" {
		if e = freshStore.(kernel.ChildExecutionStore).FinishChildExecution(t.Context(), grant); !errors.Is(e, kernel.ErrExecutionConflict) {
			t.Fatalf("receipt alone retired unmarked scope: %v", e)
		}
		assertUnchanged()
	}
	e = fresh.ReconcileRootPhysicalExecution(t.Context(), req.ID)
	if unknownHost {
		if !errors.Is(e, agent.ErrRootOutcomeUnknown) {
			t.Fatalf("receipt retired unknown Host: %v", e)
		}
		same, e := freshRT.LookupChildExecution(t.Context(), req.ID)
		if e != nil || same != grant {
			t.Fatal("unknown Host scope lost")
		}
		retained, e := freshRT.LookupOwnedPhysicalCallbackReturn(t.Context(), grant)
		if e != nil || retained != completion {
			t.Fatal("unknown Host completion changed")
		}
		assertUnchanged()
		return
	}
	if e != nil {
		t.Fatalf("original settlement could not recover: %v", e)
	}
	if _, e = freshRT.LookupChildExecution(t.Context(), req.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("scope not retired: %v", e)
	}
	if e = fresh.ReconcileRootPhysicalExecution(t.Context(), req.ID); !errors.Is(e, kernel.ErrNotFound) {
		t.Fatalf("duplicate reconciliation invented work: %v", e)
	}
	assertUnchanged()
}
