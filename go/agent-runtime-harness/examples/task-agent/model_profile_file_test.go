package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// Explicitly provision the loopback Host's independent model catalog.
// Never derive these grants from user-provided baseline stdin fields.
func certifyBaselineModelForTest(t *testing.T, modelID string, efforts ...string) {
	t.Helper()
	catalog := model.StaticModelProfiles{
		modelID: {
			StructuredJSONSchema:     true,
			EffortLevels:             append([]string(nil), efforts...),
			SupportsForcedToolChoice: true,
		},
	}
	raw, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "host-model-profiles.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(hostModelProfilesFileEnv, path)
}

// The baseline's user-selected effort is a REQUEST, not independently
// verified Host metadata. In particular, requesting a strict-schema probe
// cannot silently certify a requested effort at the same time.
func TestBaselineUnverifiedUserEffortRejectedBeforeHTTP(t *testing.T) {
	for _, protocol := range []string{"openai", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			t.Setenv(hostModelProfilesFileEnv, "")
			var physical atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				physical.Add(1)
				http.Error(w, "synthetic provider", http.StatusBadRequest)
			}))
			defer server.Close()
			input := baselineHTTPInput(protocol, server.URL+"/probe", true)
			input["model"] = "unverified-external-model"
			input["reasoningEffort"] = "max"
			code, events, _ := executeHTTPBaseline(t, input)
			preflight := baselineEventsOfType(events, "preflight")
			if code == 0 || physical.Load() != 0 || len(preflight) != 1 || preflight[0]["failureCode"] != "capability_unavailable" {
				t.Fatalf("user effort self-certified without Host catalog: code=%d HTTP=%d preflight=%+v", code, physical.Load(), preflight)
			}
		})
	}
}

// The Host's separate, file-provisioned catalog is authoritative over the
// user's preference; a lower supported level cannot be broadened by stdin.
func TestBaselineHostFileRejectsUncertifiedEffort(t *testing.T) {
	for _, protocol := range []string{"openai", "anthropic"} {
		t.Run(protocol, func(t *testing.T) {
			certifyBaselineModelForTest(t, "host-verified-low-only", "low")
			var physical atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				physical.Add(1)
				http.Error(w, "should not be called", http.StatusBadRequest)
			}))
			defer server.Close()
			input := baselineHTTPInput(protocol, server.URL+"/probe", false)
			input["model"] = "host-verified-low-only"
			input["reasoningEffort"] = "max"
			code, events, _ := executeHTTPBaseline(t, input)
			preflight := baselineEventsOfType(events, "preflight")
			if code == 0 || physical.Load() != 0 || len(preflight) != 1 || preflight[0]["failureCode"] != "capability_unavailable" {
				t.Fatalf("user effort overrode trusted Host capability: code=%d HTTP=%d preflight=%+v", code, physical.Load(), preflight)
			}
		})
	}
}

func TestHostModelProfileFileRejectsInvalidOrAmbiguousAuthority(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"duplicate-model-key", `{"one":{"EffortLevels":["high"]},"one":{"EffortLevels":["low"]}}`},
		{"folded-feature-key", `{"one":{"SupportsForcedToolChoice":true,"supportsforcedtoolchoice":false}}`},
		{"unknown-feature", `{"one":{"WeirdProviderCapability":true}}`},
		{"wrong-type", `{"one":{"SupportsForcedToolChoice":"true"}}`},
		{"null-document", `null`},
		{"array-document", `[]`},
		{"trailing-document", `{"one":{}} {"other":{}}`},
		{"oversize", strings.Repeat(" ", maxHostModelProfilesFileBytes+1)},
		{"invalid-effort", `{"one":{"EffortLevels":["unknown"]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "trusted-source.json")
			if err := os.WriteFile(path, []byte(tc.raw), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv(hostModelProfilesFileEnv, path)
			cfg := settings{mode: "live", provider: "openai", apiKey: "synthetic-key", modelID: "one"}
			if _, err := cfg.newModel(); err == nil || strings.Contains(err.Error(), path) {
				t.Fatalf("invalid Host source accepted or leaked path: err=%v", err)
			}
		})
	}
	t.Run("missing-file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing-catalog.json")
		t.Setenv(hostModelProfilesFileEnv, path)
		cfg := settings{mode: "live", provider: "openai", apiKey: "synthetic-key", modelID: "one"}
		if _, err := cfg.newModel(); err == nil || strings.Contains(err.Error(), path) {
			t.Fatalf("missing Host source silently bypassed: %v", err)
		}
	})
}
