package harness

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestContextBaseAgentOnlyNilSeedAdmissionAndConfigGolden(t *testing.T) {
	seed := &ContextSeed{
		SourcePath: []string{"source-1"}, Instructions: "history instructions",
		Entries: []runtimecontext.Entry{{ID: "entry-1", TurnID: "source-turn", SourceID: "source-1", Required: true,
			Message: model.Message{Role: model.RoleUser, Content: model.TextParts("hello")}}},
	}
	request := StartRequest{
		HostThread: HostRef{Kind: "conversation", ID: "legacy-thread"},
		HostTurn:   HostRef{Kind: "conversation_turn", ID: "legacy-user-turn"},
		Actor:      kernel.ActorRef{TenantID: "tenant", ActorID: "owner"},
		Thread:     kernel.ThreadRef{Kind: "conversation", ID: "legacy-thread"},
		RequestID:  "legacy-request", Goal: "hello", Context: seed,
	}
	config := ConfigSnapshot{DelegationPolicy: DelegationPolicySnapshot{MaxDepth: 4}, AgentSpec: AgentSpec{Model: "fixture", Instructions: "static instructions"}}
	normalized, err := sealStartAdmission(request, &config, rootAdmission{})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := SealConfigSnapshot("legacy-turn", config, time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	seedJSON, err := json.Marshal(normalized.Context)
	if err != nil {
		t.Fatal(err)
	}
	// The AgentSpec hard cut deliberately changes the private Config hash,
	// but original Turn/Invocation admission identity remains unchanged.
	const oldSeedJSON = `{"sourcePath":["source-1"],"instructions":"history instructions","entries":[{"id":"entry-1","turnID":"source-turn","sourceID":"source-1","required":true,"message":{"role":"user","content":[{"kind":"text","text":"hello"}]}}]}`
	if string(seedJSON) != oldSeedJSON || sealed.AdmissionHash != "a7c7328824eb2ed3fe7cc9243f6464560db74da600595c875fb7aa426be6368c" ||
		sealed.ContentHash != "2c13df1482a95bde1565fa901559177d2e534e8de0b4ebfacc4c5a41a80570f5" || sealed.ID != "hcfg_2c13df1482a95bde1565fa90" {
		t.Fatalf("legacy nil fields changed admission or Config identity: seed=%s admission=%s config=%s id=%s", seedJSON, sealed.AdmissionHash, sealed.ContentHash, sealed.ID)
	}
	raw, err := json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"contextBase"`) {
		t.Fatal("absent legacy ContextBase was persisted as new metadata")
	}
	var restored ConfigSnapshot
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.ContextBase != nil {
		t.Fatal("legacy Config acquired a known-none binding on JSON restore")
	}
	if _, err := canonicalSealedConfig(restored); err != nil {
		t.Fatalf("legacy Config no longer verifies after Store JSON roundtrip: %v", err)
	}
}

func contextBaseIntegrityBinding() *ContextBaseBinding {
	return &ContextBaseBinding{
		Source: &runtimecontext.WorkingStateSource{
			CheckpointID: "ctxc_" + strings.Repeat("a", 32), ContentHash: strings.Repeat("b", 64),
			CoveredThroughSourceID: "source-1", CoveredPathHash: strings.Repeat("c", 64),
		},
		SourceDelta: true, ExpectedHeadCheckpointID: "ctxc_" + strings.Repeat("d", 32),
	}
}

func TestContextBaseSealedIdentityClonesAndAuthenticatesEveryPlanField(t *testing.T) {
	input := contextBaseIntegrityBinding()
	sealed, err := SealConfigSnapshot("bound-turn", ConfigSnapshot{ContextBase: input}, time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	want := cloneContextBaseBinding(sealed.ContextBase)
	input.Source.ContentHash = strings.Repeat("e", 64)
	copy := cloneConfigSnapshot(sealed)
	copy.ContextBase.Source.CoveredThroughSourceID = "changed clone"
	if !reflect.DeepEqual(sealed.ContextBase, want) {
		t.Fatal("sealed Config retained mutable caller or clone Source storage")
	}
	store := NewMemoryStore()
	returned, _, err := store.PutConfigSnapshot(t.Context(), sealed)
	if err != nil {
		t.Fatal(err)
	}
	returned.ContextBase.Source.ContentHash = strings.Repeat("f", 64)
	loaded, err := store.GetConfigSnapshot(t.Context(), sealed.ID)
	if err != nil || !reflect.DeepEqual(loaded.ContextBase, want) {
		t.Fatalf("Store result mutation changed the frozen Context plan: %v", err)
	}
	for name, mutate := range map[string]func(*ContextBaseBinding){
		"source checkpoint": func(b *ContextBaseBinding) { b.Source.CheckpointID = "ctxc_" + strings.Repeat("e", 32) },
		"source content":    func(b *ContextBaseBinding) { b.Source.ContentHash = strings.Repeat("e", 64) },
		"source boundary":   func(b *ContextBaseBinding) { b.Source.CoveredThroughSourceID = "source-2" },
		"source path":       func(b *ContextBaseBinding) { b.Source.CoveredPathHash = strings.Repeat("e", 64) },
		"delta mode":        func(b *ContextBaseBinding) { b.SourceDelta = false },
		"cache reset":       func(b *ContextBaseBinding) { b.ResetCacheIdentity = true },
		"expected head":     func(b *ContextBaseBinding) { b.ExpectedHeadCheckpointID = "ctxc_" + strings.Repeat("e", 32) },
	} {
		t.Run(name, func(t *testing.T) {
			changed := cloneConfigSnapshot(sealed)
			mutate(changed.ContextBase)
			if _, err := canonicalSealedConfig(changed); !errors.Is(err, ErrConflict) {
				t.Fatalf("modified plan retained original sealed authority: %v", err)
			}
			newSeal, err := SealConfigSnapshot(changed.TurnID, changed, changed.CreatedAt)
			if err != nil || newSeal.ContentHash == sealed.ContentHash || newSeal.ID == sealed.ID {
				t.Fatalf("changed plan was not part of Config identity: %v", err)
			}
		})
	}
	raw, err := json.Marshal(sealed)
	if err != nil {
		t.Fatal(err)
	}
	var restored ConfigSnapshot
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if _, err := canonicalSealedConfig(restored); err != nil || !reflect.DeepEqual(restored.ContextBase, want) {
		t.Fatalf("Store JSON roundtrip lost sealed plan semantics: %v", err)
	}
}

func TestContextBaseKnownNoneDiffersFromLegacyAbsentMetadata(t *testing.T) {
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	absent, err := SealConfigSnapshot("same-turn", ConfigSnapshot{}, now)
	if err != nil {
		t.Fatal(err)
	}
	knownNone, err := SealConfigSnapshot("same-turn", ConfigSnapshot{ContextBase: &ContextBaseBinding{}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if knownNone.ID == absent.ID || knownNone.ContentHash == absent.ContentHash {
		t.Fatal("known no previous checkpoint aliased unrecorded legacy provenance")
	}
	raw, err := json.Marshal(knownNone)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"contextBase":{}`) {
		t.Fatalf("known-none marker was omitted from Store JSON: %s", raw)
	}
	var restored ConfigSnapshot
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.ContextBase == nil || restored.ContextBase.Source != nil {
		t.Fatal("Store JSON lost the known-none versus absent distinction")
	}
	if _, err := canonicalSealedConfig(restored); err != nil {
		t.Fatalf("known-none Config failed verification after roundtrip: %v", err)
	}
}

func TestContextBaseRejectsMalformedSourceAndPlanBeforeSealing(t *testing.T) {
	for name, mutate := range map[string]func(*ContextBaseBinding){
		"delta without source": func(b *ContextBaseBinding) { b.Source = nil; b.ExpectedHeadCheckpointID = "" },
		"head without source":  func(b *ContextBaseBinding) { b.Source = nil; b.SourceDelta = false },
		"bad head ID":          func(b *ContextBaseBinding) { b.ExpectedHeadCheckpointID = "foreign-head" },
		"bad source ID":        func(b *ContextBaseBinding) { b.Source.CheckpointID = "ctxc_short" },
		"uppercase source ID":  func(b *ContextBaseBinding) { b.Source.CheckpointID = "ctxc_" + strings.Repeat("A", 32) },
		"short content hash":   func(b *ContextBaseBinding) { b.Source.ContentHash = strings.Repeat("a", 63) },
		"nonhex content hash":  func(b *ContextBaseBinding) { b.Source.ContentHash = strings.Repeat("g", 64) },
		"uppercase path hash":  func(b *ContextBaseBinding) { b.Source.CoveredPathHash = strings.Repeat("A", 64) },
		"empty boundary":       func(b *ContextBaseBinding) { b.Source.CoveredThroughSourceID = "" },
		"padded boundary":      func(b *ContextBaseBinding) { b.Source.CoveredThroughSourceID = " source-1" },
		"invalid UTF8":         func(b *ContextBaseBinding) { b.Source.CoveredThroughSourceID = string([]byte{0xff}) },
	} {
		t.Run(name, func(t *testing.T) {
			binding := contextBaseIntegrityBinding()
			mutate(binding)
			if _, err := SealConfigSnapshot("bad-plan", ConfigSnapshot{ContextBase: binding}, time.Now()); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("malformed plan obtained a sealed Config: %v", err)
			}
		})
	}
}

func TestContextBasePreservesExistingLargeSourceIdentityDomain(t *testing.T) {
	sourceID := strings.Repeat("s", runtimecontext.MaxWorkingStateTextBytes+1)
	checkpoint, err := runtimecontext.NewManager(runtimecontext.Dependencies{}).Open(t.Context(), runtimecontext.OpenRequest{
		ScopeID: "scope", StaticFingerprint: "static", SourcePath: []string{sourceID},
		Entries: []runtimecontext.Entry{{ID: "entry", SourceID: sourceID, Message: model.Message{Role: model.RoleUser, Content: model.TextParts("source")}}},
	})
	if err != nil {
		t.Fatalf("ordinary Context unexpectedly limits SourceID to WorkingState text size: %v", err)
	}
	binding := &ContextBaseBinding{Source: &runtimecontext.WorkingStateSource{
		CheckpointID: checkpoint.ID, ContentHash: checkpoint.ContentHash,
		CoveredThroughSourceID: checkpoint.CoveredThroughSourceID, CoveredPathHash: checkpoint.CoveredPathHash,
	}, SourceDelta: true, ExpectedHeadCheckpointID: checkpoint.ID}
	if err := validateContextBaseBinding(binding); err != nil {
		t.Fatalf("derived binding narrowed the existing SourceID domain: %v", err)
	}
	if _, err := SealConfigSnapshot("large-source", ConfigSnapshot{ContextBase: binding}, time.Now()); err != nil {
		t.Fatalf("ordinary large SourceID cannot be frozen: %v", err)
	}
	if err := runtimecontext.ValidateWorkingState(&runtimecontext.WorkingState{Version: 1, Source: *binding.Source}); !errors.Is(err, runtimecontext.ErrInvalidInput) {
		t.Fatalf("explicit WorkingState lost its separate text bound: %v", err)
	}
}
