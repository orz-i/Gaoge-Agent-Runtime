package harnesssqlite_test

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesssqlite "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-sqlite"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile/encrypted"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-sqlite/profile/testprofile"
)

func TestNamedAgentPrivateSkillsSurviveEncryptedHarnessSchemaV3Reopen(t *testing.T) {
	ctx := t.Context()
	var key [32]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	defer clear(key[:])
	path := filepath.Join(testprofile.Dir(t), "Named Agent Encrypted #148.sqlite")
	open := encrypted.WithKey(&key)
	store, err := harnesssqlite.CreateNewWith(ctx, path, open)
	if err != nil {
		t.Fatal(err)
	}
	selected := harness.ConfigSnapshot{

		DelegationPolicy: harness.DelegationPolicySnapshot{MaxDepth: 2, NamedAgents: []harness.NamedAgentSnapshot{{MemberID: "auditor", MemberRevision: "2026-11-23", AgentSpec: harness.AgentSpec{Model: "private-model",
			ToolKeys: []string{"audit.read"},
			Skills:   []harness.SkillSnapshot{{ID: "sensitive", Revision: 11, Title: "Audit", Markdown: "Private audit instructions stored only in the encrypted Host database"}}}},
		}}, AgentSpec: harness.AgentSpec{Model: "root-model", Instructions: "root instructions"},
	}
	original, err := harness.SealConfigSnapshot("encrypted-turn", selected, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, fresh, err := store.PutConfigSnapshot(ctx, original); err != nil || !fresh {
		t.Fatalf("sealed Named Agent config write=%v fresh=%t", err, fresh)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("Private audit instructions")) || bytes.Contains(raw, []byte("SQLite format 3")) {
		t.Fatal("private Named Agent Skill leaked into encrypted SQLite bytes")
	}
	store, err = harnesssqlite.OpenExistingWith(ctx, path, open)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	reopened, err := store.GetConfigSnapshot(ctx, original.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.ID != original.ID || reopened.ContentHash != original.ContentHash ||
		len(reopened.DelegationPolicy.NamedAgents) != 1 ||
		reopened.DelegationPolicy.NamedAgents[0].Skills[0].ContentHash != original.DelegationPolicy.NamedAgents[0].Skills[0].ContentHash ||
		reopened.DelegationPolicy.NamedAgents[0].Skills[0].Markdown != original.DelegationPolicy.NamedAgents[0].Skills[0].Markdown {
		t.Fatalf("encrypted member Skill or config identity changed on reopen: %+v", reopened)
	}
	changed := selected
	changed.DelegationPolicy.NamedAgents = append([]harness.NamedAgentSnapshot(nil), selected.DelegationPolicy.NamedAgents...)
	changed.DelegationPolicy.NamedAgents[0].Skills = append([]harness.SkillSnapshot(nil), selected.DelegationPolicy.NamedAgents[0].Skills...)
	changed.DelegationPolicy.NamedAgents[0].Skills[0].Markdown = "changed private instructions"
	replacement, err := harness.SealConfigSnapshot("encrypted-turn", changed, time.Now().UTC())
	if err != nil || replacement.ID == original.ID {
		t.Fatalf("private Skill mutation retained old immutable Config identity: %v", err)
	}
}
