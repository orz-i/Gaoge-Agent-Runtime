package provider_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/anthropic"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestNativeClientsRejectUnresolvedHostContentWithoutNetwork(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	openAI, err := openai.New(openai.Config{APIKey: "test", Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	anth, err := anthropic.New(anthropic.Config{APIKey: "test", Endpoint: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	for _, client := range []model.Client{openAI, anth} {
		base := model.Request{RunID: "r", Model: "fixture", Messages: []model.Message{{Role: model.RoleUser, Content: model.TextParts("hello")}}}
		for _, change := range []func(*model.Request){
			func(r *model.Request) { r.PromptCacheKey = "cache" },
			func(r *model.Request) { r.PreviousResponseID = "response" },
			func(r *model.Request) {
				r.Messages[0] = model.Message{Role: model.RoleTool, ToolCallID: "call", ToolResult: &model.ToolResultMetadata{Name: "tool", Status: "failed", Error: "failed"}}
			},
			func(r *model.Request) { r.Messages[0].CacheControl = &model.CacheControl{Type: "ephemeral"} },
			func(r *model.Request) { r.Messages[0].Content[0].CacheControl = &model.CacheControl{Type: "ephemeral"} },
			func(r *model.Request) {
				r.Messages[0] = model.Message{Role: model.RoleSystem, Content: []model.ContentPart{{Kind: model.ContentText, Text: "instruction", CacheControl: &model.CacheControl{Type: "ephemeral"}}}}
			},
			func(r *model.Request) {
				r.Messages[0].Content = []model.ContentPart{{Kind: model.ContentVideo, MediaType: "video/mp4", Source: &model.MediaSource{Kind: model.MediaSourceArtifact, ArtifactID: "a", SHA256: strings.Repeat("a", 64), SizeBytes: 1}}}
			},
		} {
			request := model.CloneRequest(base)
			change(&request)
			if _, err := client.Generate(context.Background(), request); err == nil {
				t.Fatal("unsupported host-only representation accepted")
			}
		}
	}
	if calls.Load() != 0 {
		t.Fatal("unsupported request reached network")
	}
}
