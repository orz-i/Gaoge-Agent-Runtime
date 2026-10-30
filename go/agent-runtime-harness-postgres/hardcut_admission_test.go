package harnesspostgres_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestHardcutAdmissionMetadataSurvivesSQLJSONAndFencesChangedTurn(t *testing.T) {
	store := newStore(t) // SQLite-backed GORM conformance for Harness PostgreSQL codec
	now := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	session := harness.Session{
		ID: "admission-session", HostThread: harness.HostRef{Kind: "conversation", ID: "admission-session"},
		Actor:    kernel.ActorRef{TenantID: "tenant", ActorID: "owner"},
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if _, _, err := store.CreateSession(t.Context(), session); err != nil {
		t.Fatal(err)
	}
	initial := harness.ConfigSnapshot{
		Model: "fixture", AdmissionHash: strings.Repeat("a", 64),
		InputMessage:  &harness.HostRef{Kind: "message", ID: "user-admission"},
		OutputMessage: &harness.HostRef{Kind: "message", ID: "assistant-admission"},
	}
	config, err := harness.SealConfigSnapshot("admission-turn", initial, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = store.PutConfigSnapshot(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	restored, err := store.GetConfigSnapshot(t.Context(), config.ID)
	if err != nil || restored.ContentHash != config.ContentHash ||
		restored.AdmissionHash != initial.AdmissionHash ||
		restored.InputMessage == nil || restored.InputMessage.ID != "user-admission" ||
		restored.OutputMessage == nil || restored.OutputMessage.ID != "assistant-admission" {
		t.Fatalf("SQL JSON roundtrip dropped frozen admission: %+v err=%v", restored, err)
	}
	turn := harness.Turn{
		ID: "admission-turn", SessionID: session.ID, HostTurn: harness.HostRef{Kind: "conversation_turn", ID: "admission-turn"},
		ConfigSnapshotID: config.ID, Status: harness.TurnAccepted,
		Revision: 1, CreatedAt: now, UpdatedAt: now,
	}
	if _, fresh, err := store.CreateTurn(t.Context(), turn); err != nil || !fresh {
		t.Fatalf("create admitted turn fresh=%v err=%v", fresh, err)
	}
	changed := initial
	changed.AdmissionHash = strings.Repeat("b", 64)
	changedConfig, err := harness.SealConfigSnapshot(turn.ID, changed, now)
	if err != nil {
		t.Fatal(err)
	}
	if changedConfig.ID == config.ID {
		t.Fatal("changing admission hash failed to affect immutable Config ID")
	}
	if _, _, err = store.PutConfigSnapshot(t.Context(), changedConfig); err != nil {
		t.Fatal(err)
	}
	turn.ConfigSnapshotID = changedConfig.ID
	if _, _, err = store.CreateTurn(t.Context(), turn); !errors.Is(err, harness.ErrConflict) {
		t.Fatalf("SQL Store admitted changed frozen contract: %v", err)
	}
}
