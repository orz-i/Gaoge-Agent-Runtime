package anthropic_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/provider/anthropic"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

var nullableMetadataDefinition = tools.Definition{
	Key: "lookup:v1", Name: "lookup",
	InputSchema: json.RawMessage(`{"type":"object","properties":{"x":{"type":"integer"}},"required":["x"]}`),
}

func TestDocumentedNullableContentFieldsAndDirectToolCaller(t *testing.T) {
	for _, caller := range []string{"null", `{"type":"direct"}`} {
		t.Run(caller, func(t *testing.T) {
			n := 0
			client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
				n++
				var request struct {
					Messages []struct {
						Role    string          `json:"role"`
						Content json.RawMessage `json:"content"`
					} `json:"messages"`
				}
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
				}
				if n == 1 {
					_, _ = fmt.Fprintf(w, `{"id":"msg-first","stop_reason":"tool_use","content":[{"type":"text","text":"Using lookup.","citations":null},{"type":"tool_use","id":"toolu_1","name":"lookup","input":{"x":1},"caller":%s,"toolset_name":null}],"usage":{"input_tokens":10,"output_tokens":7}}`, caller)
				} else {
					if len(request.Messages) != 3 {
						t.Errorf("provider transcript was not preserved")
					} else {
						content := string(request.Messages[1].Content)
						// Outbound Messages ToolUseBlockParam must omit explicit
						// null caller/toolset metadata even if the response had it.
						if strings.Contains(content, `"citations"`) || strings.Contains(content, `"toolset_name"`) ||
							(strings.Contains(content, `"caller"`) != (caller != "null")) {
							t.Errorf("nullable metadata not normalized safely for input: %s", content)
						}
						if caller != "null" && !strings.Contains(content, `"caller":{"type":"direct"}`) {
							t.Errorf("validated direct caller not preserved")
						}
					}
					_, _ = fmt.Fprint(w, `{"id":"msg-final","stop_reason":"end_turn","content":[{"type":"text","text":"Done"}],"usage":{"input_tokens":15,"output_tokens":2}}`)
				}
			})
			request := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "run lookup"}}, Tools: []tools.Definition{nullableMetadataDefinition}}
			first, err := client.Generate(context.Background(), request)
			if err != nil || len(first.ToolCalls) != 1 || first.ProviderReplay == nil {
				t.Fatalf("legal tool metadata rejected: %+v, err=%v", first, err)
			}
			request.Messages = append(request.Messages, model.Message{
				Role: model.RoleAssistant, Content: first.Content, ToolCalls: first.ToolCalls, ProviderReplay: first.ProviderReplay,
			}, model.Message{Role: model.RoleTool, ToolCallID: "toolu_1", Content: `{"ok":true}`})
			second, err := client.Generate(context.Background(), request)
			if err != nil || second.Content != "Done" || n != 2 {
				t.Fatalf("legal content not replayed: %+v err=%v n=%d", second, err, n)
			}
		})
	}
}

func TestDocumentedDirectCallerStreamingBlockReplayed(t *testing.T) {
	count := 0
	client := newClient(t, func(w http.ResponseWriter, r *http.Request) {
		count++
		var request struct {
			Messages []struct {
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		if count == 2 {
			if len(request.Messages) != 3 || !strings.Contains(string(request.Messages[1].Content), `"caller":{"type":"direct"}`) {
				t.Errorf("stream caller not replayed in assistant blocks")
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(kind, data string) {
			_, _ = fmt.Fprintln(w, "event: "+kind)
			_, _ = fmt.Fprintln(w, "data: "+data)
			_, _ = fmt.Fprintln(w)
		}
		emit("message_start", fmt.Sprintf(`{"type":"message_start","message":{"id":"m%d","usage":{"input_tokens":8,"output_tokens":0}}}`, count))
		if count == 1 {
			emit("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"","citations":null}}`)
			emit("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Using lookup."}}`)
			emit("content_block_stop", `{"type":"content_block_stop","index":0}`)
			emit("content_block_start", `{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"lookup","input":{},"caller":{"type":"direct"}}}`)
			emit("content_block_delta", `{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"x\":1}"}}`)
			emit("content_block_stop", `{"type":"content_block_stop","index":1}`)
			emit("message_delta", `{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":7}}`)
		} else {
			emit("content_block_start", `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`)
			emit("content_block_delta", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Done"}}`)
			emit("content_block_stop", `{"type":"content_block_stop","index":0}`)
			emit("message_delta", `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`)
		}
		emit("message_stop", `{"type":"message_stop"}`)
	})
	request := model.Request{Messages: []model.Message{{Role: model.RoleUser, Content: "run lookup"}}, Tools: []tools.Definition{nullableMetadataDefinition}}
	first, err := client.GenerateStream(context.Background(), request, nil)
	if err != nil || len(first.ToolCalls) != 1 || first.ProviderReplay == nil {
		t.Fatalf("direct streaming caller rejected: %+v err=%v", first, err)
	}
	request.Messages = append(request.Messages, model.Message{
		Role: model.RoleAssistant, Content: first.Content, ToolCalls: first.ToolCalls, ProviderReplay: first.ProviderReplay,
	}, model.Message{Role: model.RoleTool, ToolCallID: "toolu_1", Content: `{"ok":true}`})
	second, err := client.GenerateStream(context.Background(), request, nil)
	if err != nil || second.Content != "Done" || count != 2 {
		t.Fatalf("stream direct caller not replayed: %+v err=%v calls=%d", second, err, count)
	}
}

func TestRejectIndirectToolCallersAndUnrepresentableAnnotations(t *testing.T) {
	cases := []struct{ name, content string }{
		{"server-caller", `[{"type":"tool_use","id":"toolu_1","name":"lookup","input":{"x":1},"caller":{"type":"code_execution_20250825","tool_id":"srvtoolu_fake"}}]`},
		{"toolset-member", `[{"type":"tool_use","id":"toolu_1","name":"lookup","input":{"x":1},"toolset_name":"browser"}]`},
		{"citations-nonempty", `[{"type":"text","text":"A source","citations":[{"type":"char_location","cited_text":"example"}]},{"type":"tool_use","id":"toolu_1","name":"lookup","input":{"x":1}}]`},
		{"unknown-extra-field", `[{"type":"tool_use","id":"toolu_1","name":"lookup","input":{"x":1},"delegated_scope":"auto"}]`},
		{"caller-direct-extra-field", `[{"type":"tool_use","id":"toolu_1","name":"lookup","input":{"x":1},"caller":{"type":"direct","tool_id":"hidden"}}]`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			client := newClient(t, func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(w, `{"id":"m","stop_reason":"tool_use","content":%s,"usage":{"input_tokens":8,"output_tokens":2}}`, test.content)
			})
			_, err := client.Generate(context.Background(), model.Request{
				Messages: []model.Message{{Role: model.RoleUser, Content: "lookup"}}, Tools: []tools.Definition{nullableMetadataDefinition},
			})
			if !errors.Is(err, anthropic.ErrUnsupported) {
				t.Fatalf("unsafe metadata was accepted: %v", err)
			}
		})
	}
}
