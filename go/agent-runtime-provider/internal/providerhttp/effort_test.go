package providerhttp

import (
	"encoding/json"
	"fmt"
	"reflect"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
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
		`{"reasoning":{"effort":"high"},"temperature":null}`,
		`{"reasoning":{"effort":"high"},"top_p":null}`,
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

func TestImportedProfilesAreIsolatedAndCanonical(t *testing.T) {
	original := model.StaticModelProfiles{"private-v1": {StructuredJSONSchema: true, EffortLevels: []string{"low", "high"}, SupportsSamplingWhenEffortOmitted: true}}
	first, err := NormalizeModelProfiles(original, []string{"low", "medium", "high"})
	if err != nil || !reflect.DeepEqual(first["private-v1"].EffortLevels, []string{"high", "low"}) {
		t.Fatalf("normalized: %+v %v", first, err)
	}
	mutated := original["private-v1"]
	mutated.EffortLevels[0] = "medium"
	original["private-v1"] = mutated
	if first["private-v1"].EffortLevels[0] != "high" {
		t.Fatal("caller mutation changed original certified levels")
	}
	for _, invalid := range []model.StaticModelProfiles{
		{"": {EffortLevels: []string{"low"}}},
		{" x ": {EffortLevels: []string{"low"}}},
		{"x": {EffortLevels: []string{"low", "low"}}},
		{"x": {EffortLevels: []string{"none"}}},
		{"x": {EffortLevels: []string{""}}},
	} {
		if _, err := NormalizeModelProfiles(invalid, []string{"low", "medium", "high"}); err == nil {
			t.Fatalf("accepted %v", invalid)
		}
	}
	calls := 0
	loader := model.ModelProfileSourceFunc(func() map[string]model.Capabilities {
		calls++
		return map[string]model.Capabilities{"external-id": {StructuredJSONSchema: true}}
	})
	if _, err := NormalizeModelProfiles(loader, []string{"high"}); err != nil || calls != 1 {
		t.Fatalf("external Host source not snapshotted once: calls=%d err=%v", calls, err)
	}
}

// Bounded snapshots prevent a Host metadata feed from accidentally adding
// unbounded model payloads to each Provider construction.
func TestImportedModelProfileSnapshotBoundsAndAbsentSource(t *testing.T) {
	var absent model.ModelProfileSource = model.ModelProfileSourceFunc(nil)
	got, err := NormalizeModelProfiles(absent, []string{"low", "high"})
	if err != nil || len(got) != 0 {
		t.Fatalf("nil model catalog must stay unverified: %+v %v", got, err)
	}
	catalog := make(model.StaticModelProfiles, 257)
	for i := 0; i < 257; i++ {
		catalog[fmt.Sprintf("external-model-%d", i)] = model.Capabilities{StructuredJSONSchema: true}
	}
	if _, err = NormalizeModelProfiles(catalog, []string{"low", "high"}); err == nil {
		t.Fatal("oversized Host catalog accepted")
	}
	delete(catalog, "external-model-256")
	if loaded, err := NormalizeModelProfiles(catalog, []string{"low", "high"}); err != nil || len(loaded) != 256 {
		t.Fatalf("bounded Host catalog rejected: %d %v", len(loaded), err)
	}
}
