package featurecontrol

import (
	"encoding/json"
	"testing"
)

type checkpointFixture struct {
	ID       string `json:"id"`
	Response string `json:"response"`
}

func TestControlProofIsBoundToRunRevisionGateStateAndCheckpoint(t *testing.T) {
	state := json.RawMessage(`{"allowance":2}`)
	cp := &checkpointFixture{ID: "checkpoint-1", Response: "approve"}
	p := Seal("run", 7, "approval", "checkpoint-1", state, cp)
	if p == nil || !p.Matches("run", 7, "approval", "checkpoint-1", state, cp) {
		t.Fatal("correct proof rejected")
	}
	tests := []struct {
		name, run, kind, gate string
		rev                   uint64
		state                 json.RawMessage
		cp                    any
	}{
		{"wrong-run", "other", "approval", "checkpoint-1", 7, state, cp},
		{"reused-revision", "run", "approval", "checkpoint-1", 8, state, cp},
		{"wrong-kind", "run", "allowance", "checkpoint-1", 7, state, cp},
		{"wrong-gate", "run", "approval", "checkpoint-2", 7, state, cp},
		{"altered-state", "run", "approval", "checkpoint-1", 7, json.RawMessage(`{"allowance":999}`), cp},
		{"altered-response", "run", "approval", "checkpoint-1", 7, state, &checkpointFixture{ID: "checkpoint-1", Response: "reject"}},
		{"removed-checkpoint", "run", "approval", "checkpoint-1", 7, state, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if p.Matches(tc.run, tc.rev, tc.kind, tc.gate, tc.state, tc.cp) {
				t.Fatalf("proof matched unrelated mutation %s", tc.name)
			}
		})
	}
	if (*Proof)(nil).Matches("run", 7, "approval", "checkpoint-1", state, cp) {
		t.Fatal("nil proof accepted")
	}
	if Seal("run", 7, "approval", "checkpoint-1", json.RawMessage(`{bad`), cp) != nil {
		t.Fatal("malformed state sealed")
	}
}
