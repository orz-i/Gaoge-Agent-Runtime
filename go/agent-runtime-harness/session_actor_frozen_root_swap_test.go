package harness_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
)

// This Store never changes the actual persisted Session or Invocation. It
// injects contradictory *read receipts*: first GetSession reports the intruder
// and first indexed GetInvocation changes the root Input+InputHash consistently.
// Later reads return the genuine, originally committed owner. Recomputing a
// hash here tests that a hash alone is not a Host authorization grant.
type auditTwoSourceOwnerSwapStore struct {
	harness.Store
	intruder     kernel.ActorRef
	sessionReads int
	rootReads    int
	itemReads    int
	itemWrites   int
}

func (s *auditTwoSourceOwnerSwapStore) GetSession(ctx context.Context, id string) (harness.Session, error) {
	value, err := s.Store.GetSession(ctx, id)
	if err != nil {
		return value, err
	}
	s.sessionReads++
	if s.sessionReads == 1 {
		value.Actor = s.intruder
	}
	return value, nil
}

func (s *auditTwoSourceOwnerSwapStore) GetInvocation(ctx context.Context, id string) (harness.Invocation, error) {
	value, err := s.Store.GetInvocation(ctx, id)
	if err != nil {
		return value, err
	}
	s.rootReads++
	if s.rootReads != 1 || value.ParentItemID != "" {
		return value, nil
	}
	var input map[string]json.RawMessage
	if json.Unmarshal(value.Input, &input) != nil {
		return harness.Invocation{}, harness.ErrConflict
	}
	rawActor, _ := json.Marshal(s.intruder)
	input["actor"] = rawActor
	changed, _ := json.Marshal(input)
	hash := sha256.Sum256(changed)
	value.Input = changed
	value.InputHash = hex.EncodeToString(hash[:])
	return value, nil
}

func (s *auditTwoSourceOwnerSwapStore) ListInvocations(ctx context.Context, turnID string) ([]harness.Invocation, error) {
	s.rootReads++
	return s.Store.ListInvocations(ctx, turnID)
}

func (s *auditTwoSourceOwnerSwapStore) ListItems(ctx context.Context, turnID string, afterSeq uint64, limit int) ([]harness.Item, error) {
	s.itemReads++
	return s.Store.ListItems(ctx, turnID, afterSeq, limit)
}

func (s *auditTwoSourceOwnerSwapStore) AppendItem(ctx context.Context, item harness.Item) (harness.Item, bool, error) {
	s.itemWrites++
	return s.Store.AppendItem(ctx, item)
}

func TestSessionFacadeRechecksActualRootAndKernelBeforePhysicalResume(t *testing.T) {
	opts := resumeTurnSessionOptions()
	legitimateActor := opts.Actor
	foreignActor := kernel.ActorRef{TenantID: "foreign", ActorID: "unrelated"}
	base := memory.NewStore()
	faults := &resumeTurnFaultStore{Store: base}
	hstore := harness.NewMemoryStore()
	clock := newResumeTurnClock()
	initialProvider := &multiContextCaptureModel{}
	initial, _ := newResumeTurnHarness(t, faults, hstore, initialProvider, clock, nil)
	original := mustResumeSession(t, initial, opts)
	_, err := original.Prompt(t.Context(), "turn", "original physical work")
	if !errors.Is(err, errResumeTurnInterrupted) || len(initialProvider.requests) != 0 {
		t.Fatalf("fixture did not leave a committed-but-undispatched ModelInvocation: %v calls=%d", err, len(initialProvider.requests))
	}
	provider := &multiContextCaptureModel{}
	spoof := &auditTwoSourceOwnerSwapStore{Store: hstore, intruder: foreignActor}
	runner, _ := newResumeTurnHarness(t, base, spoof, provider, clock, nil)
	opts.Actor, opts.Transcript = foreignActor, nil
	foreign := mustResumeSession(t, runner, opts)
	checks := []struct {
		name string
		call func() error
	}{
		{"load", func() error { _, err := foreign.Load(t.Context(), "turn"); return err }},
		{"state", func() error { _, err := foreign.LoadState(t.Context(), "turn"); return err }},
		{"timeline", func() error { _, err := foreign.Timeline(t.Context(), "turn", 0, 50); return err }},
		{"refresh", func() error { _, err := foreign.Refresh(t.Context(), "turn"); return err }},
		{"resume", func() error { _, err := foreign.Resume(t.Context(), "turn"); return err }},
	}
	for _, test := range checks {
		t.Run(test.name, func(t *testing.T) {
			spoof.sessionReads, spoof.rootReads = 0, 0
			spoof.itemReads, spoof.itemWrites = 0, 0
			if err := test.call(); !errors.Is(err, harness.ErrConflict) || len(provider.requests) != 0 {
				t.Fatalf("two-stage forged owner read bypassed trust boundary: err=%v physical=%d", err, len(provider.requests))
			}
			if spoof.rootReads == 0 || (test.name != "state" && spoof.rootReads < 2) || spoof.sessionReads < 2 {
				t.Fatalf("did not cross the two read boundaries: root=%d session=%d", spoof.rootReads, spoof.sessionReads)
			}
			if test.name == "state" && spoof.rootReads != 1 {
				t.Fatalf("bounded LoadState scanned root Invocations rather than one indexed GetInvocation: %d", spoof.rootReads)
			}
			if test.name == "refresh" && (spoof.itemReads != 0 || spoof.itemWrites != 0) {
				t.Fatalf("foreign Refresh accessed or repaired Timeline before final root owner proof: reads=%d writes=%d", spoof.itemReads, spoof.itemWrites)
			}
		})
	}
	// No unauthorized call consumed the original frozen grant.
	trueRunner, _ := newResumeTurnHarness(t, base, hstore, provider, clock, nil)
	opts.Actor = legitimateActor
	trueOwner := mustResumeSession(t, trueRunner, opts)
	completed, err := trueOwner.Resume(t.Context(), "turn")
	if err != nil || completed.Turn.Status != harness.TurnCompleted || len(provider.requests) != 1 {
		t.Fatalf("legitimate owner could not resume preserved original work: err=%v turn=%+v calls=%d", err, completed.Turn, len(provider.requests))
	}
}
