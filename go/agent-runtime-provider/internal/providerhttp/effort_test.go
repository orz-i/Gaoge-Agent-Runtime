package providerhttp

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestParseModelOptionsFailClosed(t *testing.T) {
	good := json.RawMessage(`{"temperature":0.5,"top_p":1,"reasoning":{"effort":"high"}}`)
	result, err := ParseOptions(good)
	if err != nil || result.Effort != "high" || result.Sampling["temperature"] != 0.5 || result.Sampling["top_p"] != 1.0 {
		t.Fatalf("good model options: %+v, %v", result, err)
	}
	for _, value := range []string{
		`null`, `[]`,
		`{"reasoning":null}`,
		`{"reasoning":{}}`,
		`{"reasoning":{"effort":"high","summary":"verbose"}}`,
		`{"reasoning":{"effort":"HIGH"}}`,
		`{"reasoning":{"effort":"auto"}}`,
		`{"reasoning":{"effort":2}}`,
		`{"reasoning":{"Effort":"high"}}`,
		`{"reasoning":{"effort":"high","effort":"low"}}`,
		`{"reasoning":{"effort":"high"},"reasoning":{"effort":"low"}}`,
		`{"reasoning":{"effort":"high"},"Reasoning":{"effort":"low"}}`,
		`{"reasoning":{"effort":"high"},"tools":[]}`,
		`{"reasoning":{"effort":"high"},"temperature":true}`,
		`{"reasoning":{"effort":"high"},"temperature":-0.1}`,
		`{"reasoning":{"effort":"high"},"top_p":1.01}`,
		`{"effort":"high"}`,
	} {
		t.Run(value, func(t *testing.T) {
			if _, err := ParseOptions(json.RawMessage(value)); err == nil {
				t.Fatal("invalid or ambiguous options accepted")
			}
		})
	}
	if options, err := ParseOptions(json.RawMessage(`{}`)); err != nil || options.Effort != "" {
		t.Fatalf("empty options: %+v %v", options, err)
	}
	if _, err := ParseOptions(json.RawMessage(`{"reasoning":{"effort":"max"}}`)); err != nil {
		t.Fatal(err)
	}
}

func TestEffortModelOverridesAreIsolatedAndCanonical(t *testing.T) {
	original := map[string][]string{"private-v1": {"high", "low"}}
	first, err := NormalizeEffortModels(original, []string{"low", "medium", "high"})
	if err != nil || !reflect.DeepEqual(first["private-v1"], []string{"high", "low"}) {
		t.Fatalf("normalized: %+v %v", first, err)
	}
	original["private-v1"][0] = "medium"
	if first["private-v1"][0] != "high" {
		t.Fatal("caller mutation changed certified levels")
	}
	for _, invalid := range []map[string][]string{
		{"": {"low"}}, {" x ": {"low"}}, {"x": nil},
		{"x": {"low", "low"}}, {"x": {"none"}}, {"x": {""}},
	} {
		if _, err := NormalizeEffortModels(invalid, []string{"low", "medium", "high"}); err == nil {
			t.Fatalf("accepted %v", invalid)
		}
	}
}
