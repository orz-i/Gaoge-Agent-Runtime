package harness

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/budget"
)

func TestWorkingStateSummaryConfigSealsPolicyAndOwnsItsStorage(t *testing.T) {
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	input := ConfigSnapshot{MaxModelOutputTokens: 128, WorkingStateSummary: &agent.WorkingStateSummaryPolicy{MaxInputTokens: 4096, MaxSerializedBytes: 16384}}
	sealed, err := SealConfigSnapshot("summary-turn", input, now)
	if err != nil {
		t.Fatal(err)
	}
	input.WorkingStateSummary.MaxInputTokens = 1
	if sealed.WorkingStateSummary.MaxInputTokens != 4096 {
		t.Fatal("sealed summary policy aliases caller input")
	}
	store := NewMemoryStore()
	if _, _, err := store.PutConfigSnapshot(t.Context(), sealed); err != nil {
		t.Fatal(err)
	}
	sealed.WorkingStateSummary.MaxSerializedBytes = 1
	loaded, err := store.GetConfigSnapshot(t.Context(), sealed.ID)
	if err != nil || loaded.WorkingStateSummary.MaxSerializedBytes != 16384 {
		t.Fatalf("stored policy aliases returned Config: %+v err=%v", loaded.WorkingStateSummary, err)
	}
	cloned := cloneConfigSnapshot(loaded)
	cloned.WorkingStateSummary.MaxInputTokens--
	if loaded.WorkingStateSummary.MaxInputTokens != 4096 {
		t.Fatal("Config clone aliases summary policy")
	}
	for _, field := range []string{"input", "bytes", "disabled"} {
		candidate := cloneConfigSnapshot(loaded)
		switch field {
		case "input":
			candidate.WorkingStateSummary.MaxInputTokens--
		case "bytes":
			candidate.WorkingStateSummary.MaxSerializedBytes--
		default:
			candidate.WorkingStateSummary = nil
		}
		changed, err := SealConfigSnapshot(candidate.TurnID, candidate, now)
		if err != nil || changed.ID == loaded.ID {
			t.Fatalf("summary %s policy was omitted from sealed identity: %v", field, err)
		}
	}
	raw, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	var restored ConfigSnapshot
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if _, err := canonicalSealedConfig(restored); err != nil {
		t.Fatalf("summary policy does not verify after persisted JSON roundtrip: %v", err)
	}
}

func TestWorkingStateSummaryConfigRejectsUnboundedAndUnsupportedCombinations(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*ConfigSnapshot)
	}{
		{"missing input limit", func(c *ConfigSnapshot) { c.WorkingStateSummary.MaxInputTokens = 0 }},
		{"negative input limit", func(c *ConfigSnapshot) { c.WorkingStateSummary.MaxInputTokens = -1 }},
		{"missing byte limit", func(c *ConfigSnapshot) { c.WorkingStateSummary.MaxSerializedBytes = 0 }},
		{"negative byte limit", func(c *ConfigSnapshot) { c.WorkingStateSummary.MaxSerializedBytes = -1 }},
		{"missing per call cap", func(c *ConfigSnapshot) { c.MaxModelOutputTokens = 0 }},
		{"shared budget", func(c *ConfigSnapshot) { c.SharedBudget = &budget.Limits{MaxLLMCalls: 4} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config := ConfigSnapshot{MaxModelOutputTokens: 128, WorkingStateSummary: &agent.WorkingStateSummaryPolicy{MaxInputTokens: 4096, MaxSerializedBytes: 16384}}
			tc.edit(&config)
			if _, err := SealConfigSnapshot("summary-turn", config, time.Now()); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("invalid summary admission accepted: %v", err)
			}
		})
	}
}
