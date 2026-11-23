package harnesspostgres_test

import (
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	harnesspostgres "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness-postgres"
)

func TestNamedAgentSnapshotRemainsFrozenAcrossIndependentSQLStoreHandles(t *testing.T) {
	original, db := newStoreWithDB(t)
	selected := harness.ConfigSnapshot{DelegationPolicy: harness.DelegationPolicySnapshot{
		MaxDepth: 1, NamedAgents: []harness.NamedAgentSnapshot{{MemberID: "writer", MemberRevision: "v9", AgentSpec: harness.AgentSpec{Model: "private-model",
			Instructions: "original approved instructions", Skills: []harness.SkillSnapshot{{ID: "private-write", Revision: 9,
				Title: "Private write", Markdown: "Do not execute unapproved writes"}}}}},
	}, AgentSpec: harness.AgentSpec{Model: "root-model"},
	}
	config, err := harness.SealConfigSnapshot("named-agent-pg-turn", selected, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, fresh, err := original.PutConfigSnapshot(t.Context(), config); err != nil || !fresh {
		t.Fatalf("write immutable named scope: %v fresh=%v", err, fresh)
	}
	separate, err := harnesspostgres.New(db)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := separate.GetConfigSnapshot(t.Context(), config.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ContentHash != config.ContentHash || len(loaded.DelegationPolicy.NamedAgents) != 1 ||
		loaded.DelegationPolicy.NamedAgents[0].Skills[0].Markdown != "Do not execute unapproved writes" ||
		loaded.DelegationPolicy.NamedAgents[0].Skills[0].ContentHash != config.DelegationPolicy.NamedAgents[0].Skills[0].ContentHash {
		t.Fatalf("SQL handle changed frozen Named Agent Skill binding: %+v", loaded)
	}
	changed := selected
	changed.DelegationPolicy.NamedAgents = append([]harness.NamedAgentSnapshot(nil), selected.DelegationPolicy.NamedAgents...)
	changed.DelegationPolicy.NamedAgents[0].Skills = append([]harness.SkillSnapshot(nil), selected.DelegationPolicy.NamedAgents[0].Skills...)
	changed.DelegationPolicy.NamedAgents[0].Skills[0].Markdown = "Modified instructions"
	different, err := harness.SealConfigSnapshot("named-agent-pg-turn", changed, time.Now().UTC())
	if err != nil || different.ID == config.ID {
		t.Fatalf("named Agent Skill content not part of original Config hash: %v", err)
	}
	recovered, err := original.GetConfigSnapshot(t.Context(), config.ID)
	if err != nil || recovered.ContentHash != config.ContentHash {
		t.Fatalf("mutated previously sealed SQL Host state: %+v %v", recovered, err)
	}
}
