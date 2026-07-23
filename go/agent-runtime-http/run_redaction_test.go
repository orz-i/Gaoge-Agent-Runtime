package http

import (
	"encoding/json"
	"strings"
	"testing"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	valueCookie21216B47 = "cookie"
)

func TestRunToolApprovalPreviewRedactsAndBoundsArguments(t *testing.T) {
	interaction := model.Interaction{
		Type:               model.InteractionApproveTool,
		RequestPayloadJSON: `{"toolName":"delete_file","toolCallID":"call_1","sideEffectLevel":"destructive","arguments":{"action":"delete","resource":"file","target":"report.pdf","password":"secret","nested":{"apiToken":"token","values":[1,2,3]}}}`,
	}
	response := runInteractionResponse(interaction)
	request, ok := response["request"].(map[string]interface{})
	if !ok {
		t.Fatalf("request=%#v", response["request"])
	}
	preview, ok := request["preview"].(map[string]interface{})
	if !ok {
		t.Fatalf("preview=%#v", request["preview"])
	}
	encoded, err := json.Marshal(preview)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if strings.Contains(text, "secret") || strings.Contains(text, "token\"") {
		t.Fatalf("sensitive value leaked: %s", text)
	}
	if preview["sideEffectLevel"] != "destructive" || preview["target"] != "report.pdf" {
		t.Fatalf("preview=%#v", preview)
	}
}

func TestRunEventDetailRedactionLimitsDepthArrayAndSize(t *testing.T) {
	items := make([]interface{}, 30)
	for i := range items {
		items[i] = strings.Repeat("x", 500)
	}
	raw, err := json.Marshal(map[string]interface{}{
		valueCookie21216B47: "private",
		"items":             items,
		"nested": map[string]interface{}{
			"a": map[string]interface{}{
				"b": map[string]interface{}{
					"c": map[string]interface{}{"password": "hidden"},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	redacted := redactedRunJSONString(string(raw))
	if len(redacted) > 4096 {
		t.Fatalf("redacted payload size=%d", len(redacted))
	}
	if strings.Contains(redacted, "private") || strings.Contains(redacted, "hidden") {
		t.Fatalf("sensitive value leaked: %s", redacted)
	}
}
