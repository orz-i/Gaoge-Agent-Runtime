package jsoncontract_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
)

func TestUniqueObjectRejectsDuplicateAndFoldedMemberNames(t *testing.T) {
	for _, raw := range []string{
		`{"effort":"high","effort":"none"}`,
		`{"effort":"high","Effort":"none"}`,
		`{"scope":{"identity":"first","IDENTITY":"second"}}`,
		`{"nested":[{"tool":"one","tool":"two"}]}`,
		`null`, `[]`, `1`, `"text"`, `{"x":1} garbage`,
		`{"x":`,
	} {
		t.Run(raw, func(t *testing.T) {
			if err := jsoncontract.ValidateUniqueJSONObject([]byte(raw)); !errors.Is(err, jsoncontract.ErrAmbiguousJSON) {
				t.Fatalf("accepted invalid/ambiguous object: %q err=%v", raw, err)
			}
		})
	}
	if err := jsoncontract.ValidateUniqueJSONObject(json.RawMessage(`{"effort":"high","nested":[{"x":1},{"x":2}]}`)); err != nil {
		t.Fatal(err)
	}
	deep := strings.Repeat("[", 260) + "1" + strings.Repeat("]", 260)
	if err := jsoncontract.ValidateUniqueJSONKeys([]byte(deep)); !errors.Is(err, jsoncontract.ErrAmbiguousJSON) {
		t.Fatalf("accepted excessively nested JSON: %v", err)
	}
}
