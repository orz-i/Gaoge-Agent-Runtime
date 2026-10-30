package agent

import (
	"encoding/json"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

func TestDispatchModelRequestHasOnlyMonotonicPermissions(t *testing.T) {
	base := model.Request{
		RunID: "frozen-run", InvocationID: "frozen-invocation", Model: "provider/model",
		ModelOptions: json.RawMessage(`{"temperature":0.5}`),
		Messages: []model.Message{{Role: model.RoleUser, Content: []model.ContentPart{
			{Kind: model.ContentText, Text: "question"},
			{Kind: model.ContentImage, MediaType: "image/png", Source: &model.MediaSource{
				Kind: model.MediaSourceURL, URL: "https://assets.example.test/frozen.png",
			}},
		}}},
		Tools: []tools.Definition{
			{Key: "tool.one", Name: "tool_one", InputSchema: json.RawMessage(`{"type":"object"}`)},
			{Key: "tool.two", Name: "tool_two", InputSchema: json.RawMessage(`{"type":"object"}`)},
		},
		HostedTools:     []model.HostedTool{{Key: "native.one", DefinitionVersion: "v1", Target: json.RawMessage(`{"scope":"narrow"}`)}},
		MaxOutputTokens: 80, RequireToolCall: true,
	}
	cap := 3
	base.HostedToolCallLimit = &cap

	for _, test := range []struct {
		name       string
		change     func(*model.Request)
		wantAccept bool
	}{
		{name: "unchanged", wantAccept: true},
		{name: "tighten_token_limit", change: func(r *model.Request) { r.MaxOutputTokens = 32 }, wantAccept: true},
		{name: "increase_token_limit", change: func(r *model.Request) { r.MaxOutputTokens = 81 }},
		{name: "remove_token_limit", change: func(r *model.Request) { r.MaxOutputTokens = 0 }},
		{name: "negative_token_limit", change: func(r *model.Request) { r.MaxOutputTokens = -1 }},
		{name: "tighten_hosted_limit", change: func(r *model.Request) { n := 0; r.HostedToolCallLimit = &n }, wantAccept: true},
		{name: "increase_hosted_limit", change: func(r *model.Request) { n := 4; r.HostedToolCallLimit = &n }},
		{name: "remove_hosted_limit", change: func(r *model.Request) { r.HostedToolCallLimit = nil }},
		{name: "negative_hosted_limit", change: func(r *model.Request) { n := -1; r.HostedToolCallLimit = &n }},
		{name: "subset_local_tools", change: func(r *model.Request) { r.Tools = r.Tools[1:] }, wantAccept: true},
		{name: "remove_local_tools_but_hosted_still_callable", change: func(r *model.Request) { r.Tools = nil; r.RequireToolCall = false }},
		{name: "remove_all_tools_and_relax", change: func(r *model.Request) { r.Tools = nil; r.HostedTools = nil; r.RequireToolCall = false }, wantAccept: true},
		{name: "rewrite_local_presentation_post_intent", change: func(r *model.Request) {
			r.Tools[0].Description = "Only the existing named tool"
			r.Tools[0].InputSchema = json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
		}},
		{name: "invalid_local_schema", change: func(r *model.Request) { r.Tools[0].InputSchema = json.RawMessage(`{bad`) }},
		{name: "relabel_local_tool", change: func(r *model.Request) { r.Tools[0].Name = "different" }},
		{name: "duplicate_local_tool", change: func(r *model.Request) { r.Tools[1] = r.Tools[0] }},
		{name: "drop_hosted_tool", change: func(r *model.Request) { r.HostedTools = nil }, wantAccept: true},
		{name: "retarget_hosted_tool", change: func(r *model.Request) { r.HostedTools[0].Target = json.RawMessage(`{"scope":"all"}`) }},
		{name: "change_hosted_version", change: func(r *model.Request) { r.HostedTools[0].DefinitionVersion = "v2" }},
		{name: "duplicate_hosted_tool", change: func(r *model.Request) { r.HostedTools = append(r.HostedTools, r.HostedTools[0]) }},
		{name: "unjustified_required_tool_relaxation", change: func(r *model.Request) { r.RequireToolCall = false }},
		{name: "relax_required_tool_after_exhaustion", change: func(r *model.Request) {
			r.Tools = nil
			limit := 0
			r.HostedToolCallLimit = &limit
			r.RequireToolCall = false
		}, wantAccept: true},
		{name: "frozen_message_rewrite", change: func(r *model.Request) { r.Messages[0].Content = model.TextParts("not frozen") }},
		{name: "frozen_media_url_change", change: func(r *model.Request) {
			r.Messages[0].Content[1].Source.URL = "https://assets.example.test/forged.png"
		}},
		{name: "frozen_media_type_change", change: func(r *model.Request) {
			r.Messages[0].Content[1].MediaType = "image/jpeg"
		}},
		{name: "frozen_media_part_reorder", change: func(r *model.Request) {
			r.Messages[0].Content[0], r.Messages[0].Content[1] = r.Messages[0].Content[1], r.Messages[0].Content[0]
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			frozen := model.CloneRequest(base)
			candidate := model.CloneRequest(frozen)
			if test.change != nil {
				test.change(&candidate)
			}
			if got := validDispatchModelRequest(frozen, candidate); got != test.wantAccept {
				t.Fatalf("validDispatchModelRequest()=%v, want=%v; candidate=%+v", got, test.wantAccept, candidate)
			}
		})
	}
}

func TestDispatchModelRequestCanIntroduceOnlyBoundedLimits(t *testing.T) {
	base := model.Request{RunID: "frozen", InvocationID: "frozen-step", Model: "provider/model"}
	for _, test := range []struct {
		name   string
		change func(*model.Request)
		want   bool
	}{
		{name: "none", want: true},
		{name: "introduce_token_ceiling", change: func(r *model.Request) { r.MaxOutputTokens = 20 }, want: true},
		{name: "introduce_zero_hosted_ceiling", change: func(r *model.Request) { n := 0; r.HostedToolCallLimit = &n }, want: true},
		{name: "introduce_positive_hosted_ceiling", change: func(r *model.Request) { n := 5; r.HostedToolCallLimit = &n }, want: true},
		{name: "escalate_require_tools", change: func(r *model.Request) { r.RequireToolCall = true }},
	} {
		t.Run(test.name, func(t *testing.T) {
			frozen := model.CloneRequest(base)
			candidate := model.CloneRequest(frozen)
			if test.change != nil {
				test.change(&candidate)
			}
			if got := validDispatchModelRequest(frozen, candidate); got != test.want {
				t.Fatalf("got=%t, want=%t", got, test.want)
			}
		})
	}
}

func TestPreIntentProjectionCannotExpandCatalogAuthority(t *testing.T) {
	base := model.CloneRequest(model.Request{
		RunID: "pre-intent-run", Model: "provider/model", Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("question")}},
		Tools:       []tools.Definition{{Key: "fixed", Name: "fixed", InputSchema: json.RawMessage(`{"type":"object"}`)}},
		HostedTools: []model.HostedTool{{Key: "native", DefinitionVersion: "1", Target: json.RawMessage(`{"allowed":true}`)}},
	})
	for _, test := range []struct {
		name   string
		change func(*model.Request)
		want   bool
	}{
		{name: "unchanged", want: true},
		{name: "project_history_and_schema", change: func(r *model.Request) {
			r.Messages = append([]model.Message{{Role: model.RoleSystem, Content: model.TextParts("trusted history")}}, r.Messages...)
			r.Tools[0].Description = "Visible frozen description"
			r.Tools[0].InputSchema = json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
		}, want: true},
		{name: "drop_local_tool", change: func(r *model.Request) { r.Tools = nil }, want: true},
		{name: "rename_local_tool", change: func(r *model.Request) { r.Tools[0].Name = "other" }},
		{name: "invalid_schema", change: func(r *model.Request) { r.Tools[0].InputSchema = json.RawMessage(`not_json`) }},
		{name: "expand_local_tools", change: func(r *model.Request) {
			r.Tools = append(r.Tools, tools.Definition{Key: "new", Name: "new", InputSchema: json.RawMessage(`{"type":"object"}`)})
		}},
		{name: "change_provider", change: func(r *model.Request) { r.Model = "other" }},
		{name: "rebind_native", change: func(r *model.Request) { r.HostedTools[0].Target = json.RawMessage(`{"allowed":false}`) }},
		{name: "change_budget", change: func(r *model.Request) { r.MaxOutputTokens = 200 }},
		{name: "empty_history", change: func(r *model.Request) { r.Messages = nil }},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := model.CloneRequest(base)
			if test.change != nil {
				test.change(&candidate)
			}
			if got := validPreparedModelRequest(base, candidate); got != test.want {
				t.Fatalf("got=%t want=%t", got, test.want)
			}
		})
	}
}
