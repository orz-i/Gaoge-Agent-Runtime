package model_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestStrictJSONOutputContractProfile(t *testing.T) {
	good := &model.OutputContract{Name: "answer", Schema: json.RawMessage(`{"type":"object","properties":{"items":{"type":"array","items":{"type":"object","properties":{"label":{"type":"string"},"count":{"type":"integer"}},"required":["label","count"],"additionalProperties":false}}},"required":["items"],"additionalProperties":false}`)}
	if err := model.ValidateOutputContract(good); err != nil {
		t.Fatal(err)
	}
	req := model.Request{OutputContract: model.CloneOutputContract(good)}
	cloned := model.CloneRequest(req)
	cloned.OutputContract.Schema[0] = '['
	if !json.Valid(req.OutputContract.Schema) {
		t.Fatal("mutated original contract through CloneRequest")
	}
	if model.ValidateOutputContract(nil) != nil {
		t.Fatal("nil contract should remain legacy text")
	}
	for name, raw := range map[string]string{
		"missing_name":                 `{"type":"object","properties":{},"required":[],"additionalProperties":false}`,
		"missing_required":             `{"type":"object","properties":{"id":{"type":"string"}},"additionalProperties":false}`,
		"missing_additionalProperties": `{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}`,
		"optional_property":            `{"type":"object","properties":{"id":{"type":"string"}},"required":[],"additionalProperties":false}`,
		"remote_ref":                   `{"$ref":"https://example.invalid/schema"}`,
		"not_object":                   `{"type":"array","items":{"type":"string"}}`,
		"nested_open":                  `{"type":"object","properties":{"nested":{"type":"object","properties":{"x":{"type":"string"}},"required":["x"]}},"required":["nested"],"additionalProperties":false}`,
		"forbidden_numeric_constraint": `{"type":"object","properties":{"count":{"type":"integer","minimum":0}},"required":["count"],"additionalProperties":false}`,
		"wrong_enum_type":              `{"type":"object","properties":{"count":{"type":"integer","enum":["not a number"]}},"required":["count"],"additionalProperties":false}`,
		"complex_enum":                 `{"type":"object","properties":{"items":{"type":"array","items":{"type":"string"},"enum":[["a"]]}},"required":["items"],"additionalProperties":false}`,
		"wrong_const_type":             `{"type":"object","properties":{"flag":{"type":"boolean","const":"not bool"}},"required":["flag"],"additionalProperties":false}`,
		"json_invalid":                 "{broken",
	} {
		t.Run(name, func(t *testing.T) {
			c := &model.OutputContract{Name: "answer", Schema: json.RawMessage(raw)}
			if name == "missing_name" {
				c.Name = ""
			}
			if err := model.ValidateOutputContract(c); !errors.Is(err, model.ErrOutputContract) {
				t.Fatalf("unsupported contract admitted: err=%v schema=%s", err, raw)
			}
		})
	}
}
