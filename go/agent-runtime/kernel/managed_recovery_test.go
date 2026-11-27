package kernel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/internal/featurecontrol"
)

func TestManagedSafePointClosedEventProfileNeedsNativeProof(t *testing.T) {
	initial, source, lease, grant, _, _ := managedReducerFixture()
	previous := source
	previous.Run.Revision--
	previous.EventHead--
	next := Record{Run: source.Run, State: source.State, Checkpoint: source.Checkpoint, Result: source.Result}
	for _, purpose := range []string{"", "context_summary"} {
		phases := map[string]string{"pending": "agent.model_invocation.pending", "completed": "agent.model_invocation.completed"}
		if purpose != "" {
			phases["consumed"] = "agent.context_summary.consumed"
		}
		for phase, event := range phases {
			t.Run(purpose+"/"+phase, func(t *testing.T) {
				point := initial.SafePoint
				o, ok := point.AgentObligations()
				if !ok {
					t.Fatal("invalid source")
				}
				o.Phase, o.Purpose = phase, purpose
				if purpose != "" {
					o.BindingDigest = strings.Repeat("c", 64)
				}
				raw, err := json.Marshal(o)
				if err != nil {
					t.Fatal(err)
				}
				point.LifecycleSource = raw
				if !point.Valid() {
					t.Fatal("common lifecycle rejected")
				}
				proof := ManagedSafePointProof{point: point, proof: featurecontrol.Seal(previous.Run.ID, previous.Run.Revision, "managed_safe_point", grant.ID, previous.State, point)}
				if !proof.MatchesMutation(previous, next, lease, grant, []EventDraft{{Type: event}}) {
					t.Fatal("exact phase/proof rejected")
				}
				if (ManagedSafePointProof{point: point}).MatchesMutation(previous, next, lease, grant, []EventDraft{{Type: event}}) {
					t.Fatal("public metadata forged proof")
				}
				for _, wrong := range []string{"run.completed", "agent.model_invocation.pending", "agent.model_invocation.completed", "agent.context_summary.consumed"} {
					if wrong != event && proof.MatchesMutation(previous, next, lease, grant, []EventDraft{{Type: wrong}}) {
						t.Fatal("event did not match phase")
					}
				}
				if proof.MatchesMutation(previous, next, lease, grant, []EventDraft{{Type: event}, {Type: event}}) {
					t.Fatal("extra events bypassed phase")
				}
				o.SourceRevision++
				point.LifecycleSource, _ = json.Marshal(o)
				mismatched := ManagedSafePointProof{point: point, proof: featurecontrol.Seal(previous.Run.ID, previous.Run.Revision, "managed_safe_point", grant.ID, previous.State, point)}
				if mismatched.MatchesMutation(previous, next, lease, grant, []EventDraft{{Type: event}}) {
					t.Fatal("source revision not bound")
				}
			})
		}
	}
	for _, retiredVersion := range []string{"managed-context-v1", "working-state-summary-v3", "working-state-summary-v2", "managed-agent-obligations-v1"} {
		retired := initial.SafePoint
		retired.LifecycleVersion = retiredVersion
		if retired.Valid() {
			t.Fatal("retired lifecycle accepted", retiredVersion)
		}
	}
}

func managedReducerFixture() (ManagedTransfer, Snapshot, ExecutionLease, ChildExecutionGrant, ManagedAuthority, time.Time) {
	now := time.Date(2026, 11, 13, 0, 0, 0, 0, time.UTC)
	a := ManagedAuthority{Namespace: strings.Repeat("01", 32), Generation: 1, Version: 1}
	h := ManagedAuthority{Namespace: strings.Repeat("02", 32), Generation: 1, Version: 1}
	s := Snapshot{Run: Run{ID: "root", Kind: "agent", Status: RunStatusRunning, Revision: 3}, State: json.RawMessage(`{"pending":"original"}`), EventHead: 4}
	l := ExecutionLease{RunID: "root", OwnerID: "old", LeaseID: "old-lease", Epoch: 1, ExpiresAt: now.Add(-time.Second)}
	id, _ := OwnedPhysicalDispatchGrantID("root", "start", "nativecmd_original-dispatch")
	g := ChildExecutionGrant{ID: id, ChildRunID: "root", ExpectedRevision: 1, ExecutionOwnerID: l.OwnerID, ExecutionLeaseID: l.LeaseID, ExecutionEpoch: l.Epoch, AdmissionSourceHash: strings.Repeat("a", 64), AdmissionOperation: "start"}
	b := ManagedHostBinding{KernelAuthority: a, HostAuthority: h, TurnID: "turn", InvocationID: "inv", Attempt: 1, RunID: "root", DispatchID: "nativecmd_original-dispatch", Phase: "start", Source: json.RawMessage(`{"actor":"original"}`)}
	p := ManagedSafePoint{ID: "safe", Host: b, Source: s, OriginalGrant: g, OriginalLease: l, ModelInvocationID: "model-2", ModelRequestHash: strings.Repeat("b", 64), LifecycleVersion: ManagedLifecycleAgentObligations}
	p.LifecycleSource, _ = json.Marshal(ManagedAgentObligations{Phase: "pending", SourceRevision: s.Run.Revision, InvocationID: p.ModelInvocationID, RequestHash: p.ModelRequestHash, SamplingDigest: strings.Repeat("c", 64), Journal: json.RawMessage(`{"actual":"returned"}`)})
	p.RecoveryID = ManagedRecoveryID(p.ID, b)
	return ManagedInitialTransfer(p), s, l, g, a, now
}
func TestManagedRecoveryReducersPreserveSourceAndLiveOwner(t *testing.T) {
	original, s, l, g, a, now := managedReducerFixture()
	q := ManagedRecoverySourceRequest{RunID: s.Run.ID, SuspensionID: original.SafePoint.ID, ExpectedRevision: s.Run.Revision}
	live := l
	live.ExpiresAt = now.Add(time.Second)
	if _, e := ValidateManagedRecoverySource(original, q, s, live, g, a, g.ExecutionEpoch, now); !errors.Is(e, ErrExecutionConflict) {
		t.Fatalf("live owner: %v", e)
	}
	changed := s
	changed.EventHead++
	if _, e := ValidateManagedRecoverySource(original, q, changed, l, g, a, g.ExecutionEpoch, now); !errors.Is(e, ErrExecutionConflict) {
		t.Fatalf("journal mismatch: %v", e)
	}
	foreign := a
	foreign.Generation++
	if _, e := ValidateManagedRecoverySource(original, q, s, l, g, foreign, g.ExecutionEpoch, now); !errors.Is(e, ErrExecutionConflict) {
		t.Fatalf("authority mismatch: %v", e)
	}
	parked, e := ValidateManagedRecoverySource(original, q, s, l, g, a, g.ExecutionEpoch, now)
	if e != nil || ManagedDigest(parked) != ManagedDigest(original) {
		t.Fatalf("park: %+v %v", parked, e)
	}

}
func TestManagedRecoveryModeAndExactDigest(t *testing.T) {
	initial, s, l, g, a, now := managedReducerFixture()
	p, e := ValidateManagedRecoverySource(initial, ManagedRecoverySourceRequest{RunID: s.Run.ID, SuspensionID: "safe", ExpectedRevision: 3}, s, l, g, a, g.ExecutionEpoch, now)
	if e != nil {
		t.Fatal(e)
	}
	r := ManagedRecoveryAuthorization{Mode: ManagedResume}
	proof := ManagedAdmissionProof{reservation: r, transfer: p, valid: true}
	proof.request = ManagedRecoverySourceRequest{RunID: s.Run.ID, SuspensionID: p.SafePoint.ID, ExpectedRevision: s.Run.Revision, ExpectedTransferDigest: ManagedDigest(p)}
	req := ExecutionAcquireRequest{RunID: s.Run.ID, OwnerID: "next", LeaseDuration: time.Minute}
	admission := proof.OwnedAdmission(req, "next-lease", "next-grant")
	v, _, e := ResolveManagedPhysicalAdmission(t.Context(), p, admission, s, l, g, a, g.ExecutionEpoch, now, nil)
	if e != nil {
		t.Fatal(e)
	}
	if !admission.Matches(v.Grant, s, v.Lease) {
		t.Fatal("native admission commitment not recoverable")
	}
	if proof.OwnedAdmission(req, "foreign-lease", "next-grant").Matches(v.Grant, s, v.Lease) {
		t.Fatal("foreign caller adopted lease")
	}

	cancelR := r
	cancelR.Mode = ManagedCancel
	cancelR.CancellationReason = "original"
	cancelP := ManagedAdmissionProof{reservation: cancelR, transfer: p, valid: true}
	if _, _, e = ResolveManagedPhysicalAdmission(t.Context(), p, cancelP.OwnedAdmission(req, "next-lease", "next-grant"), s, l, g, a, g.ExecutionEpoch, now, nil); !errors.Is(e, ErrExecutionConflict) {
		t.Fatalf("CANCEL enabled physical dispatch: %v", e)
	}
	cq := ManagedCancelRequest{RunID: s.Run.ID, SuspensionID: "safe", Selection: cancelP}
	cancelled, m, e := ManagedCancellationMutation(p, cq, s, now)
	if e != nil {
		t.Fatal(e)
	}
	if m.Record.Run.Status != RunStatusCancelled || ManagedDigest(m.Record.State) != ManagedDigest(s.State) || cancelled.Terminal.EventHead != s.EventHead+1 {
		t.Fatal("cancel changed original evidence")
	}
	if !ManagedCancelReplay(cancelled, cq, *cancelled.Terminal) {
		t.Fatal("cancel ACK replay rejected")
	}
	forged := cancelP
	forged.reservation.CancellationReason = "different"
	cq.Selection = forged
	if ManagedCancelReplay(cancelled, cq, *cancelled.Terminal) {
		t.Fatal("foreign cancellation receipt adopted")
	}
	cq.Selection = proof
	if _, _, e = ManagedCancellationMutation(p, cq, s, now); !errors.Is(e, ErrExecutionConflict) {
		t.Fatalf("RESUME became cancellation: %v", e)
	}
}
func TestManagedTransferWireRejectsRetiredAuthority(t *testing.T) {
	original, _, _, _, _, _ := managedReducerFixture()
	for _, selection := range []ManagedRecoveryAuthorization{{}, {Mode: ManagedResume}, {Mode: ManagedCancel, CancellationReason: "original"}} {
		original.Selection = selection
		raw, err := json.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		var restored ManagedTransfer
		if err = json.Unmarshal(raw, &restored); err != nil || !restored.Valid() || ManagedDigest(restored) != ManagedDigest(original) {
			t.Fatalf("native wire changed: %v", err)
		}
		for _, retired := range []string{`"State":"INVALIDATED"`, `"Generation":1`, `"ClaimantID":"old"`, `"Activations":[]`} {
			if err := json.Unmarshal(append([]byte("{"+retired+","), raw[1:]...), &restored); err == nil {
				t.Fatalf("retired transfer field accepted: %s", retired)
			}
		}
		for _, retired := range []string{`"ID":"reservation"`, `"RecoveryID":"old"`, `"SuspensionID":"old"`, `"Binding":{}`, `"Revision":1`, `"Cancellation":{"Payload":null}`} {
			legacy := bytes.Replace(raw, []byte(`"Selection":{`), []byte(`"Selection":{`+retired+","), 1)
			if err := json.Unmarshal(legacy, &restored); err == nil {
				t.Fatalf("retired selection field accepted: %s", retired)
			}
		}
	}
	if (ManagedRecoveryAuthorization{Mode: ManagedResume, CancellationReason: "foreign"}).Valid() ||
		(ManagedRecoveryAuthorization{Mode: ManagedCancel, CancellationReason: " noncanonical "}).Valid() {
		t.Fatal("invalid native selection accepted")
	}
}

type managedConfiguredKernel struct {
	Store
	ManagedRecoveryStore
	OwnedPhysicalExecutionStore
	transfer ManagedTransfer
	enable   OwnedPhysicalAdmissionResult
}

func (s *managedConfiguredKernel) ManagedRecoveryAuthority(context.Context) (ManagedAuthority, error) {
	return s.transfer.SafePoint.Host.KernelAuthority, nil
}
func (s *managedConfiguredKernel) LoadManagedRecovery(context.Context, string) (ManagedTransfer, error) {
	return managedClone(s.transfer), nil
}
func (s *managedConfiguredKernel) BeginOwnedPhysicalExecution(_ context.Context, p OwnedPhysicalAdmission) (OwnedPhysicalAdmissionResult, error) {
	s.enable = p.Candidate()
	point := s.transfer.SafePoint
	return ResolveOwnedPhysicalAdmission(point.Source, point.OriginalLease, point.OriginalLease.ExpiresAt.Add(time.Millisecond), false, false, p)
}

type managedConfiguredHost struct {
	binding ManagedHostBinding
	calls   int
}

func (s *managedConfiguredHost) ManagedRecoveryAuthority(context.Context) (ManagedAuthority, error) {
	s.calls++
	return s.binding.HostAuthority, nil
}
func (s *managedConfiguredHost) VerifyManagedOriginalDispatch(_ context.Context, b ManagedHostBinding) error {
	s.calls++
	if !reflect.DeepEqual(b, s.binding) {
		return ErrExecutionConflict
	}
	return nil
}

type managedForbiddenIDSource struct{ calls int }

func (s *managedForbiddenIDSource) NewID(string) (string, error) {
	s.calls++
	return "", errors.New("configured entropy called before Leave")
}

func TestManagedRecoveryUsesOnlyConstructionBoundHostAndInternalEntropy(t *testing.T) {
	initial, s, l, g, a, now := managedReducerFixture()
	parked, e := ValidateManagedRecoverySource(initial, ManagedRecoverySourceRequest{RunID: s.Run.ID, SuspensionID: initial.SafePoint.ID, ExpectedRevision: s.Run.Revision}, s, l, g, a, g.ExecutionEpoch, now)
	if e != nil {
		t.Fatal(e)
	}
	host := &managedConfiguredHost{binding: parked.SafePoint.Host}
	counterfeit := &managedConfiguredHost{binding: host.binding}
	counterfeit.binding.Source = json.RawMessage(`{"foreign":true}`)
	store := &managedConfiguredKernel{transfer: parked}
	ids := &managedForbiddenIDSource{}
	rt, e := New(Dependencies{Store: store, IDs: ids, ManagedRecoveryHost: host})
	if e != nil {
		t.Fatal(e)
	}
	if !rt.UsesManagedRecoveryHost(host) || rt.UsesManagedRecoveryHost(counterfeit) {
		t.Fatal("runtime accepted foreign per-call port")
	}
	bound, e := rt.BindManagedDispatch(t.Context(), host.binding)
	if e != nil {
		t.Fatal(e)
	}
	if _, ok := rt.ManagedDispatch(bound, s.Run.ID); !ok {
		t.Fatal("actual bound dispatch lost")
	}
	// A deliberately reconstructed Runtime is a different trusted configuration,
	// and may not borrow the prior Runtime's bound actual-call context.
	other, e := New(Dependencies{Store: store, ManagedRecoveryHost: counterfeit})
	if e != nil {
		t.Fatal(e)
	}
	if _, ok := other.ManagedDispatch(bound, s.Run.ID); ok {
		t.Fatal("different construction borrowed original binding")
	}
	proof := parked
	selected, e := rt.AuthorizeManagedContinuation(t.Context(), proof)
	if e != nil {
		t.Fatal(e)
	}
	if selected.Authorization().Mode != ManagedResume || counterfeit.calls != 0 {
		t.Fatal("unconfigured Host minted selection")
	}
	_, _, e = rt.AdmitManagedContinuation(t.Context(), selected, "next-owner", time.Minute)
	if e != nil {
		t.Fatal(e)
	}
	if ids.calls != 0 || store.enable.Lease.LeaseID == "" || store.enable.Grant.ID == "" || store.enable.Lease.LeaseID == store.enable.Grant.ID {
		t.Fatal("pre-Leave entropy was configurable or unbound")
	}
	// A native admission has already frozen the choice. Re-admission must
	// never consult a second Host selection, even when its projection differs.
	store.transfer.Selection = selected.Authorization()
	store.transfer.Revision++
	calls := host.calls
	readmitted, e := rt.AuthorizeManagedContinuation(t.Context(), managedClone(store.transfer))
	if e != nil || !reflect.DeepEqual(readmitted.Authorization(), selected.Authorization()) || host.calls != calls+1 {
		t.Fatalf("native choice was re-elected by Host: %v, host reads=%d", e, host.calls-calls)
	}
	if _, e = rt.AuthorizeManagedContinuation(t.Context(), proof); !errors.Is(e, ErrExecutionConflict) {
		t.Fatalf("stale transfer accepted: %v", e)
	}
	unconfigured, e := New(Dependencies{Store: store})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = unconfigured.AuthorizeManagedContinuation(t.Context(), proof); !errors.Is(e, ErrExecutionConflict) {
		t.Fatalf("unconfigured proof issuer: %v", e)
	}
}

func TestManagedQualifiedParkRetainsExactFirstOwnerSource(t *testing.T) {
	original, source, lease, grant, authority, now := managedReducerFixture()
	original.SafePoint.LifecycleVersion = ManagedLifecycleAgentObligations
	o := ManagedAgentObligations{Phase: "pending", SourceRevision: source.Run.Revision, InvocationID: original.SafePoint.ModelInvocationID,
		RequestHash: original.SafePoint.ModelRequestHash, SamplingDigest: strings.Repeat("c", 64), InputBinding: "queue", Journal: json.RawMessage(`{"prepared":true}`)}
	original.SafePoint.LifecycleSource, _ = json.Marshal(o)
	current := source
	current.Run.Revision++
	current.EventHead++
	current.State = json.RawMessage(`{"claimed":"original"}`)
	next := original.SafePoint
	next.ID = "qualified"
	next.RecoveryID = ManagedRecoveryID(next.ID, next.Host)
	next.Source = current
	o.SourceRevision = current.Run.Revision
	o.RequalifiedFrom = original.SafePoint.ID
	next.LifecycleSource, _ = json.Marshal(o)
	q := ManagedRecoverySourceRequest{RunID: current.Run.ID, SuspensionID: original.SafePoint.ID, ExpectedRevision: current.Run.Revision,
		Qualification: ManagedFrontierQualification{prior: original, point: next, proof: featurecontrol.Seal(current.Run.ID, current.Run.Revision, "managed_qualified_recovery", original.SafePoint.ID, current.State, next)}}
	parked, err := ValidateManagedRecoverySource(original, q, current, lease, grant, authority, grant.ExecutionEpoch, now)
	if err != nil || !parked.Valid() || parked.Revision != current.Run.Revision || ManagedDigest(parked.SafePoint) != ManagedDigest(next) {
		t.Fatalf("qualified park: %v", err)
	}
	ordinary := q
	ordinary.Qualification = ManagedFrontierQualification{}
	forged := q
	forged.Qualification.proof = nil
	live := lease
	live.ExpiresAt = now.Add(time.Second)
	changed := current
	changed.EventHead++
	stale := original
	stale.Revision++
	legacy := original
	legacy.SafePoint.LifecycleVersion = "managed-context-v1"
	for _, c := range []struct {
		name    string
		prior   ManagedTransfer
		request ManagedRecoverySourceRequest
		source  Snapshot
		owner   ExecutionLease
	}{
		{"ordinary", original, ordinary, current, lease}, {"public metadata", original, forged, current, lease},
		{"live lease", original, q, current, live}, {"changed source", original, q, changed, lease},
		{"stale frontier", stale, q, current, lease}, {"legacy proof", legacy, q, current, lease},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ValidateManagedRecoverySource(c.prior, c.request, c.source, c.owner, grant, authority, grant.ExecutionEpoch, now); !errors.Is(err, ErrExecutionConflict) {
				t.Fatalf("unqualified park: %v", err)
			}
		})
	}
}

func TestManagedPreparationSafePointRequiresExactNativeProof(t *testing.T) {
	initial, source, lease, grant, _, _ := managedReducerFixture()
	previous := source
	previous.Run.Revision--
	previous.EventHead--
	next := Record{Run: source.Run, State: source.State, Checkpoint: source.Checkpoint, Result: source.Result}
	point := initial.SafePoint
	point.LifecycleVersion = ManagedLifecycleAgentObligations
	point.ModelInvocationID, point.ModelRequestHash = "", ""
	obligation := ManagedAgentObligations{
		Phase: "prepare", SourceRevision: source.Run.Revision,
		InputBinding: "original-queue", InputCursor: 1,
		PreparationContext: &ManagedPreparationContext{CheckpointID: "ctx-source", CheckpointDigest: strings.Repeat("c", 64)},
		Journal:            json.RawMessage(`{"hydrated":true}`),
	}
	set := func(p ManagedSafePoint, o ManagedAgentObligations) ManagedSafePoint {
		raw, err := json.Marshal(o)
		if err != nil {
			t.Fatal(err)
		}
		p.LifecycleSource = raw
		return p
	}
	point = set(point, obligation)
	if !point.Valid() {
		t.Fatal("pre-Model proof required a fabricated invocation")
	}
	proof := ManagedSafePointProof{point: point, proof: featurecontrol.Seal(previous.Run.ID, previous.Run.Revision, "managed_safe_point", grant.ID, previous.State, point)}
	// New points bind the entire exact event (including payload and wakeup).
	// Older ready/input evidence cannot authorize a new transition class.
	for _, event := range []string{"agent.native_preparation.ready", "agent.input_applied", "agent.model_continued", "agent.completion_corrected", "tool.completed", "tool.correction_requested", "agent.allowance_auto_granted"} {
		events := []EventDraft{{Type: event, Message: "original", Wakeup: true}}
		originalEvent := obligation
		originalEvent.PreparationTransitionDigest = ManagedDigest(events)
		target := set(point, originalEvent)
		qualified := ManagedSafePointProof{point: target, proof: featurecontrol.Seal(previous.Run.ID, previous.Run.Revision, "managed_safe_point", grant.ID, previous.State, target)}
		if !qualified.MatchesMutation(previous, next, lease, grant, events) {
			t.Fatalf("rejected exact native phase: %s", event)
		}
		if (ManagedSafePointProof{point: target}).MatchesMutation(previous, next, lease, grant, events) {
			t.Fatalf("public metadata forged native proof: %s", event)
		}
		changed := []EventDraft{{Type: event, Message: "different", Wakeup: true}}
		if qualified.MatchesMutation(previous, next, lease, grant, changed) ||
			qualified.MatchesMutation(previous, next, lease, grant, []EventDraft{{Type: "agent.input_applied", Message: "other"}}) {
			t.Fatalf("proof authorized another transition: %s", event)
		}
		wantLegacy := event == "agent.native_preparation.ready" || event == "agent.input_applied"
		if proof.MatchesMutation(previous, next, lease, grant, events) != wantLegacy {
			t.Fatalf("legacy preparation scope changed: %s", event)
		}
	}
	for _, event := range []string{"agent.model_invocation.pending", "agent.model_invocation.claimed", "agent.model_invocation.dispatch_intent", "agent.model_invocation.completed", "agent.context_summary.consumed", "tool.batch_requested", "tool.authorization_started", "tool.authorization_committed", "tool.dispatch_intent", "tool.pending", "tool.capacity_waiting", "interaction.created", "agent.budget_paused", "run.completed"} {
		events := []EventDraft{{Type: event}}
		other := obligation
		other.PreparationTransitionDigest = ManagedDigest(events)
		target := set(point, other)
		qualified := ManagedSafePointProof{point: target, proof: featurecontrol.Seal(previous.Run.ID, previous.Run.Revision, "managed_safe_point", grant.ID, previous.State, target)}
		if qualified.MatchesMutation(previous, next, lease, grant, events) {
			t.Fatalf("preparation proof authorized another event: %s", event)
		}
	}
	for _, events := range [][]EventDraft{
		nil,
		{{Type: "tool.completed"}, {Type: "tool.dispatch_intent"}},
		{{Type: "agent.input_applied"}, {Type: "agent.model_continued"}},
	} {
		other := obligation
		other.PreparationTransitionDigest = ManagedDigest(events)
		target := set(point, other)
		qualified := ManagedSafePointProof{point: target, proof: featurecontrol.Seal(previous.Run.ID, previous.Run.Revision, "managed_safe_point", grant.ID, previous.State, target)}
		if qualified.MatchesMutation(previous, next, lease, grant, events) {
			t.Fatalf("preparation proof authorized ambiguous transition: %+v", events)
		}
	}
	for _, change := range []func(*ManagedSafePoint, *ManagedAgentObligations){
		func(_ *ManagedSafePoint, o *ManagedAgentObligations) { o.PreparationContext = nil },
		func(_ *ManagedSafePoint, o *ManagedAgentObligations) {
			o.PreparationContext = &ManagedPreparationContext{CheckpointID: "ctx-source", CheckpointDigest: "bad"}
		},
		func(p *ManagedSafePoint, o *ManagedAgentObligations) {
			p.ModelInvocationID = "fake"
			o.InvocationID = "fake"
		},
		func(p *ManagedSafePoint, o *ManagedAgentObligations) {
			p.ModelRequestHash = strings.Repeat("b", 64)
			o.RequestHash = p.ModelRequestHash
		},
		func(_ *ManagedSafePoint, o *ManagedAgentObligations) { o.SamplingDigest = strings.Repeat("b", 64) },
		func(_ *ManagedSafePoint, o *ManagedAgentObligations) { o.PreparationTransitionDigest = "invalid" },
		func(_ *ManagedSafePoint, o *ManagedAgentObligations) { o.RequalifiedFrom = "old-point" },
		func(_ *ManagedSafePoint, o *ManagedAgentObligations) { o.SourceRevision++ },
		func(_ *ManagedSafePoint, o *ManagedAgentObligations) { o.Phase = "pending" },
		func(p *ManagedSafePoint, _ *ManagedAgentObligations) { p.LifecycleVersion = "managed-context-v1" },
	} {
		other, altered := point, obligation
		change(&other, &altered)
		other = set(other, altered)
		if other.Valid() {
			t.Fatalf("invalid pre-Model phase accepted: %+v", altered)
		}
	}
}

func TestManagedSafePointBindsExactPhysicalDispatch(t *testing.T) {
	original, _, _, _, _, _ := managedReducerFixture()
	for _, phase := range []string{"start", "start-retry", "resume", "resolve-approval"} {
		t.Run(phase, func(t *testing.T) {
			p := managedClone(original.SafePoint)
			p.Host.Phase = phase
			if phase == "start-retry" {
				p.Host.Attempt = 2
			}
			p.Host.DispatchID = "nativecmd_original-" + phase
			if phase == "resolve-approval" {
				p.Host.Approval = &ManagedApprovalDecision{CheckpointID: "checkpoint", Decision: "approve", Comment: "original grant"}
			}
			op, ok := ManagedDispatchOperation(phase)
			if !ok {
				t.Fatal("missing native phase")
			}
			p.OriginalGrant.AdmissionOperation = op
			p.OriginalGrant.ID, _ = OwnedPhysicalDispatchGrantID(p.Host.RunID, op, p.Host.DispatchID)
			p.RecoveryID = ManagedRecoveryID(p.ID, p.Host)
			if !p.Valid() {
				t.Fatal("exact original dispatch rejected")
			}
			for _, mutate := range []func(*ManagedSafePoint){
				func(q *ManagedSafePoint) { q.Host.DispatchID = "foreign" },
				func(q *ManagedSafePoint) {
					q.Host.DispatchID = "retired-host-dispatch"
					q.OriginalGrant.ID, _ = OwnedPhysicalDispatchGrantID(q.Host.RunID, op, q.Host.DispatchID)
				},
				func(q *ManagedSafePoint) { q.OriginalGrant.ID = "foreign" },
				func(q *ManagedSafePoint) { q.OriginalGrant.AdmissionOperation = "managed_resume" },
				func(q *ManagedSafePoint) {
					if q.Host.Phase == "start-retry" {
						q.Host.Attempt = 1
					} else {
						q.Host.Phase = "start-retry"
					}
				},
				func(q *ManagedSafePoint) { q.Host.Phase = "resolve-subtask-approval" },
				func(q *ManagedSafePoint) { q.Host.Phase = "handoff-start" },
				func(q *ManagedSafePoint) {
					if q.Host.Approval == nil {
						q.Host.Approval = &ManagedApprovalDecision{CheckpointID: "foreign", Decision: "approve"}
					} else {
						q.Host.Approval = nil
					}
				},
			} {
				bad := managedClone(p)
				mutate(&bad)
				bad.RecoveryID = ManagedRecoveryID(bad.ID, bad.Host)
				if bad.Valid() {
					t.Fatalf("foreign or malformed point accepted: %+v", bad)
				}
			}
		})
	}
}
