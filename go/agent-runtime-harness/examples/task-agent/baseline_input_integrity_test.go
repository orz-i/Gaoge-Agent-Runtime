package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestBaselineInputRejectsShadowedAndExplicitlyEmptyEffort(t *testing.T) {
	original, err := json.Marshal(unitBaselineInput())
	if err != nil {
		t.Fatal(err)
	}
	prefix := strings.TrimSuffix(string(original), "}")
	for _, tc := range []struct {
		name, suffix string
	}{
		{"null", `,"reasoningEffort":null}`},
		{"empty", `,"reasoningEffort":""}`},
		{"alias-upper", `,"reasoningEffort":"high","ReasoningEffort":"none"}`},
		{"alias-lower", `,"reasoningEffort":"high","reasoningeffort":"none"}`},
		{"alias-only", `,"ReasoningEffort":"high"}`},
		{"same-key-twice", `,"reasoningEffort":"high","reasoningEffort":"none"}`},
		{"unknown", `,"unexpected":"high"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, projected, err := readBaselineInput(strings.NewReader(prefix + tc.suffix))
			if err == nil {
				t.Fatalf("unsafe baseline accepted ambiguous input: %+v %+v", got, projected)
			}
			var output bytes.Buffer
			if code := runBaseline(t.Context(), strings.NewReader(prefix+tc.suffix), &output); code != 2 || strings.Count(output.String(), "\n") != 1 ||
				!strings.Contains(output.String(), `"failureCode":"invalid_configuration"`) {
				t.Fatalf("unsafe input crossed configuration gate: code=%d output=%s", code, output.String())
			}
		})
	}
	// Omission remains compatible with prior baseline configs; a
	// well-formed explicit effort is copied exactly into the report.
	if _, _, err := readBaselineInput(bytes.NewReader(original)); err != nil {
		t.Fatalf("old omitted-effort config rejected: %v", err)
	}
	withEffort := prefix + `,"reasoningEffort":"high"}`
	input, public, err := readBaselineInput(strings.NewReader(withEffort))
	if err != nil || input.ReasoningEffort != "high" || public.ReasoningEffort != "high" {
		t.Fatalf("valid option lost: %q %q err=%v", input.ReasoningEffort, public.ReasoningEffort, err)
	}
}
