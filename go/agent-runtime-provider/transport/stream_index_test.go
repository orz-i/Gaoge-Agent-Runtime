package transport

import (
	"encoding/json"
	"errors"
	"math"
	"testing"
)

func TestStreamIndexBoundsAreArchitectureIndependent(t *testing.T) {
	for _, value := range []any{int(0), int64(1), float64(12), json.Number("65535")} {
		got, err := streamIndex(value)
		if err != nil || got < 0 || got > maxStreamIndex {
			t.Fatalf("valid index rejected %v: %v", value, err)
		}
	}
	for _, value := range []any{nil, true, "1", float64(0.5), float64(-1), math.NaN(), math.Inf(1), json.Number("1.5"), json.Number("9223372036854775808"), json.Number("4294967296"), json.Number("9223372036854775807"), json.Number("1e999999"), json.Number("-1"), int64(65536), float64(65536)} {
		if _, err := streamIndex(value); !errors.Is(err, errInvalidStreamIndex) {
			t.Fatalf("invalid index accepted %#v: %v", value, err)
		}
	}
	if index, err := streamIndexField(map[string]any{}, "index"); err != nil || index != 0 {
		t.Fatal("missing optional index changed")
	}
}

func TestMalformedStreamIndexCannotGrowToolOutput(t *testing.T) {
	for _, value := range []any{json.Number("9223372036854775807"), float64(1.5), float64(-1), nil} {
		result := &GenerateOutput{}
		parsed := map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"tool_calls": []any{map[string]any{"index": value, "id": "call"}}}}}}
		if err := mergeChatStreamToolCalls(parsed, result); !errors.Is(err, errInvalidStreamIndex) {
			t.Fatal("bad tool index did not fail", err)
		}
		if len(result.ToolCalls) != 0 {
			t.Fatal("invalid index mutated output")
		}
		if _, err := anthropicContentBlockIndex(map[string]any{"index": value}); !errors.Is(err, errInvalidStreamIndex) {
			t.Fatal("bad content block index did not fail")
		}
	}
}
