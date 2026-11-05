package providerhttp

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/jsoncontract"
)

// Provider-owned role/status/Tool keys are authority-bearing. Reject duplicate
// object members at every depth, including case-folded and Unicode-escaped aliases,
// before encoding/json can overwrite the earlier physical evidence.
func TestValidateUniqueJSONKeys(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{name: "simple", raw: `{"role":"assistant","content":[{"type":"tool_use","id":"t1","input":{"x":1}}]}`, valid: true},
		{name: "same_member_in_distinct_objects", raw: `{"output":[{"type":"message"},{"type":"function_call"}]}`, valid: true},
		{name: "case_variations_in_distinct_objects", raw: `{"output":[{"Role":"user"},{"role":"assistant"}]}`, valid: true},
		{name: "escaped_text_is_not_object_member", raw: `{"role":"assistant","text":"\\\"role\\\":\\\"user\\\"","escape":"\\u0061"}`, valid: true},
		{name: "scalars_and_empty_objects", raw: `[{},{"items":[null,1,2,true,false,"x"]}]`, valid: true},
		{name: "duplicate_outer_owner", raw: `{"role":"user","role":"assistant"}`},
		{name: "duplicate_same_value", raw: `{"role":"assistant","role":"assistant"}`},
		{name: "duplicate_nested_tool", raw: `{"content":[{"type":"tool_use","id":"bad","id":"good"}]}`},
		{name: "duplicate_escaped_name", raw: `{"role":"user","\u0072ole":"assistant"}`},
		{name: "folded_outer_role", raw: `{"Role":"user","role":"assistant"}`},
		{name: "folded_outer_status", raw: `{"Status":"incomplete","status":"completed"}`},
		{name: "folded_same_value", raw: `{"Object":"response","object":"response"}`},
		{name: "folded_nested_tool", raw: `{"output":[{"Call_ID":"forged","call_id":"trusted"}]}`},
		{name: "folded_unicode_kelvin", raw: `{"Key":"first","Key":"second"}`},
		{name: "folded_unicode_long_s", raw: `{"status":"incomplete","ſtatus":"completed"}`},
		{name: "folded_unicode_escaped_long_s", raw: `{"status":"incomplete","\u017Ftatus":"completed"}`},
		{name: "repeated_object_members_deeper", raw: `{"a":[{"b":{"status":"incomplete","status":"completed"}}]}`},
		{name: "non_json", raw: `garbage`},
		{name: "trailing_json", raw: `{}{}`},
		{name: "truncated", raw: `{"role":"assistant"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateUniqueJSONKeys([]byte(tc.raw))
			if tc.valid && err != nil || !tc.valid && !errors.Is(err, ErrProtocol) {
				t.Fatalf("unexpected JSON authority classification: valid=%t err=%v", tc.valid, err)
			}
		})
	}
	t.Run("deep_nesting_is_bounded", func(t *testing.T) {
		raw := bytes.Repeat([]byte("["), jsoncontract.MaxUniqueJSONDepth+1)
		raw = append(raw, '0')
		raw = append(raw, bytes.Repeat([]byte("]"), jsoncontract.MaxUniqueJSONDepth+1)...)
		if !errors.Is(ValidateUniqueJSONKeys(raw), ErrProtocol) {
			t.Fatal("unbounded provider JSON nesting")
		}
	})
}

func TestValidateUniqueJSONObject(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{name: "empty_object", raw: `{}`, valid: true},
		{name: "nested_object", raw: ` {"args":{"x":[1,true]}} `, valid: true},
		{name: "array", raw: `[]`},
		{name: "null", raw: `null`},
		{name: "number", raw: `1`},
		{name: "boolean", raw: `false`},
		{name: "string", raw: `"text"`},
		{name: "duplicate_object_key", raw: `{"x":1,"x":2}`},
		{name: "folded_object_key", raw: `{"X":1,"x":2}`},
		{name: "trailing_json", raw: `{}{}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateUniqueJSONObject([]byte(tc.raw))
			if tc.valid && err != nil || !tc.valid && !errors.Is(err, ErrProtocol) {
				t.Fatalf("unexpected physical object classification: valid=%t err=%v", tc.valid, err)
			}
		})
	}
}

func TestReadJSONRejectsAmbiguousOwnerBeforeUnmarshal(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		reject    bool
	}{
		{name: "clean", raw: `{"object":"response","id":"clean"}`},
		{name: "duplicate", raw: `{"object":"chat.completion","object":"response","id":"ambiguous"}`, reject: true},
		{name: "nested_duplicate", raw: `{"object":"response","output":[{"call_id":"x","call_id":"y"}]}`, reject: true},
		{name: "folded_owner", raw: `{"Object":"image","object":"response"}`, reject: true},
		{name: "folded_nested", raw: `{"object":"response","output":[{"Call_ID":"x","call_id":"y"}]}`, reject: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{Body: io.NopCloser(bytes.NewBufferString(tc.raw))}
			var decoded struct {
				Object string `json:"object"`
				ID     string `json:"id"`
			}
			err := ReadJSON(resp, &decoded)
			if tc.reject {
				if !errors.Is(err, ErrProtocol) || decoded.Object != "" {
					t.Fatalf("unsafe decoded duplicate: %+v err=%v", decoded, err)
				}
			} else if err != nil || decoded.Object != "response" || decoded.ID != "clean" {
				t.Fatalf("legitimate response rejected: %+v err=%v", decoded, err)
			}
		})
	}
}
