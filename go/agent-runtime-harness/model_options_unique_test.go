package harness_test

import (
	"encoding/json"
	"testing"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
)

func TestModelOptionsAreUniqueBeforeFrozenConfigHash(t *testing.T) {
	for _, options := range []string{
		`{"reasoning":{"effort":"high"},"reasoning":{"effort":"none"}}`,
		`{"reasoning":{"effort":"high","effort":"none"}}`,
		`{"temperature":0.5,"temperature":0.1}`,
		`{"reasoning":{"effort":"high"},"Reasoning":{"effort":"none"}}`,
		`{"reasoning":{"effort":"high","Effort":"none"}}`,
		`{"reasoning":{"effort":"high"},"reasoning":[{"effort":"low","effort":"high"}]}`,
	} {
		t.Run(options, func(t *testing.T) {
			_, err := harness.SealConfigSnapshot("audit-turn", harness.ConfigSnapshot{
				Model: "gpt-6-sol", ModelOptions: json.RawMessage(options),
			}, time.Now().UTC())
			if err == nil {
				t.Fatal("ambiguous ModelOptions were normalized before freezing")
			}
			_, err = harness.SealConfigSnapshot("audit-turn", harness.ConfigSnapshot{
				Model: "gpt-6-sol",
				Roles: []harness.RoleSnapshot{{
					ID: "role", Revision: 1, Name: "Role", ModelOptions: json.RawMessage(options),
				}},
			}, time.Now().UTC())
			if err == nil {
				t.Fatal("ambiguous Role ModelOptions were normalized before freezing")
			}
		})
	}
	accepted := json.RawMessage(`{"reasoning":{"effort":"high"},"temperature":0.25}`)
	sealed, err := harness.SealConfigSnapshot("audit-turn", harness.ConfigSnapshot{
		Model: "gpt-6-sol", ModelOptions: accepted,
	}, time.Now().UTC())
	if err != nil || len(sealed.ContentHash) != 64 || !json.Valid(sealed.ModelOptions) {
		t.Fatalf("valid canonical ModelOptions failed to seal: %+v %v", sealed, err)
	}
}

func TestModelOptionsRequireObjectBeforeSealing(t *testing.T) {
	for _, options := range []string{`null`, `[]`, `1`, `"text"`, `true`} {
		t.Run(options, func(t *testing.T) {
			_, err := harness.SealConfigSnapshot("audit-turn", harness.ConfigSnapshot{
				Model: "gpt-6-sol", ModelOptions: json.RawMessage(options),
			}, time.Now().UTC())
			if err == nil {
				t.Fatal("non-object model options accepted as sealed authority")
			}
		})
	}
}
