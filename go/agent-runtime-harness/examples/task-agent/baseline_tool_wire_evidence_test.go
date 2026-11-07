package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func wireFixtureFrame(event string, payload string) string {
	return fmt.Sprintf("event: %s\ndata: %s\n\n", event, payload)
}

func syntheticProbeSSE(doneCipher, finalCipher string, includeFinal bool) string {
	done := fmt.Sprintf(`{"type":"response.output_item.done","output_index":0,"item":{"type":"reasoning","id":"rs-fixture","summary":[],"encrypted_content":%q}}`, doneCipher)
	body := wireFixtureFrame("response.output_item.done", done)
	if !includeFinal {
		return body
	}
	response := fmt.Sprintf(`{"type":"response.completed","response":{"object":"response","id":"response-fixture","status":"completed","output":[{"type":"reasoning","id":"rs-fixture","summary":[],"encrypted_content":%q},{"type":"function_call","id":"fc-fixture","status":"completed","call_id":"call-fixture","name":"baseline_probe_echo","arguments":"{\"value\":\"probe\"}"}],"usage":{"input_tokens":9,"output_tokens":3}}}`, finalCipher)
	return body + wireFixtureFrame("response.completed", response)
}

func TestToolProbeWireEvidenceOnlyComparesFinalizedReasoningCiphertext(t *testing.T) {
	for _, tc := range []struct {
		name, done, final string
		hasFinal          bool
		expected          *bool
		encrypted         bool
	}{
		{"consistent", "sealed-A", "sealed-A", true, ptrBool(true), true},
		{"mismatch", "sealed-A", "sealed-B", true, ptrBool(false), true},
		{"no_terminal", "sealed-A", "", false, nil, true},
		{"no_done_cipher", "", "sealed-B", true, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture := &preflightWireCapture{}
			payload := syntheticProbeSSE(tc.done, tc.final, tc.hasFinal)
			capture.observe([]byte(payload), io.EOF)
			observer := &preflightHTTPObserver{responseWire: capture}
			evidence := observeToolProbeWireEvidence(observer)
			if evidence == nil || !evidence.ReasoningDone || evidence.EncryptedDone != tc.encrypted ||
				evidence.TerminalSeen != tc.hasFinal ||
				(evidence.DoneFinalMatch == nil) != (tc.expected == nil) ||
				evidence.DoneFinalMatch != nil && *evidence.DoneFinalMatch != *tc.expected {
				t.Fatalf("incorrect content-free evidence: %+v", evidence)
			}
			encoded, err := json.Marshal(evidence)
			if err != nil || strings.Contains(string(encoded), "sealed-A") || strings.Contains(string(encoded), "sealed-B") ||
				strings.Contains(string(encoded), "rs-fixture") {
				t.Fatalf("unsafe wire evidence in report: %s err=%v", encoded, err)
			}
		})
	}
}

func ptrBool(value bool) *bool { return &value }

func TestOpenAIToolProbeContinuesWithAuthoritativeDoneCiphertext(t *testing.T) {
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Header().Set("Content-Type", "text/event-stream")
		if requests == 1 {
			_, _ = io.WriteString(w, syntheticProbeSSE("server-secret-A", "server-secret-B", true))
			return
		}
		var payload struct {
			Store   bool                         `json:"store"`
			Input   []map[string]json.RawMessage `json:"input"`
			Include []string                     `json:"include"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.Store || len(payload.Include) != 1 || payload.Include[0] != "reasoning.encrypted_content" ||
			len(payload.Input) != 4 || string(payload.Input[1]["encrypted_content"]) != `"server-secret-A"` {
			t.Error("stateless continuation did not carry the completed done ciphertext")
		}
		final := `{"object":"response","id":"resp-final-probe","status":"completed","output":[{"type":"message","role":"assistant","phase":"final_answer","content":[{"type":"output_text","text":"{\"ok\":true}"}]}],"usage":{"input_tokens":7,"output_tokens":2}}`
		_, _ = io.WriteString(w, wireFixtureFrame("response.completed", `{"type":"response.completed","response":`+final+`}`))
	}))
	defer server.Close()
	config := settings{mode: "live", provider: "openai", modelID: "quality-fixture", endpoint: server.URL,
		apiKey: "test-key-never-export", outputTokens: 1024, reasoningEffort: "medium",
		modelProfiles: model.StaticModelProfiles{"quality-fixture": {
			StructuredJSONSchema: true, EffortLevels: []string{"medium"}, SupportsForcedToolChoice: true,
		}},
	}
	probe := baselineToolReplayProbe(context.Background(), config)
	if probe.Status != "passed" || probe.Stage != "completed" || probe.ModelCalls != 2 ||
		probe.HTTPRequests != 2 || requests != 2 || !probe.ReasoningSeen ||
		!probe.EncryptedReplaySeen || probe.WireEvidence != nil || !probe.Usage.Complete ||
		probe.Usage.KnownCalls != 2 {
		t.Fatalf("completed done ciphertext did not authorize a validated continuation: status=%s stage=%s code=%s modelCalls=%d httpCalls=%d replay=%t usage=%+v",
			probe.Status, probe.Stage, probe.FailureCode, probe.ModelCalls, requests,
			probe.EncryptedReplaySeen, probe.Usage)
	}
	encoded, err := json.Marshal(probe)
	if err != nil || strings.Contains(string(encoded), "server-secret-A") ||
		strings.Contains(string(encoded), "server-secret-B") ||
		strings.Contains(string(encoded), "test-key-never-export") {
		t.Fatal("successful tool probe report exposed ciphertext or credentials")
	}
}
