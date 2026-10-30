package model_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestMessageOrderedMediaContractAndDurableClone(t *testing.T) {
	original := model.Message{Role: model.RoleUser, Content: []model.ContentPart{
		{Kind: model.ContentText, Text: "Before image"},
		{Kind: model.ContentImage, MediaType: "image/png", Source: &model.MediaSource{
			Kind: model.MediaSourceURL, URL: "https://media.example.test/image.png",
		}},
		{Kind: model.ContentText, Text: "After image"},
		{Kind: model.ContentFile, MediaType: "application/pdf", Source: &model.MediaSource{
			Kind: model.MediaSourceFileID, FileID: "file-pdf-1",
		}},
	}}
	if err := model.ValidateMessage(original); err != nil {
		t.Fatal(err)
	}
	if _, ok := model.PlainText(original.Content); ok {
		t.Fatal("multimodal content incorrectly flattened into plain text")
	}
	request := model.Request{RunID: "run", Messages: []model.Message{original}}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	var persisted model.Request
	if err = json.Unmarshal(raw, &persisted); err != nil || !reflect.DeepEqual(request, persisted) {
		t.Fatalf("ordered input blocks did not survive exact persistence: err=%v persisted=%+v", err, persisted)
	}
	cloned := model.CloneRequest(request)
	cloned.Messages[0].Content[1].Source.URL = "https://changed.example.test/image.png"
	cloned.Messages[0].Content[2].Text = "changed"
	if request.Messages[0].Content[1].Source.URL != "https://media.example.test/image.png" ||
		request.Messages[0].Content[2].Text != "After image" {
		t.Fatal("media pointer or text clone leaked into original frozen request")
	}
}

func TestMessageMediaFailsClosedOnMalformedPartsAndUnauthorizedRoles(t *testing.T) {
	validImage := model.ContentPart{Kind: model.ContentImage, MediaType: "image/jpeg",
		Source: &model.MediaSource{Kind: model.MediaSourceURL, URL: "https://media.example.test/image.jpg"}}
	tests := []struct {
		name    string
		message model.Message
	}{
		{"unknown-kind", model.Message{Role: model.RoleUser, Content: []model.ContentPart{{Kind: "video", Text: "x"}}}},
		{"embedded-base64", model.Message{Role: model.RoleUser, Content: []model.ContentPart{{
			Kind: model.ContentImage, MediaType: "image/png", Source: &model.MediaSource{Kind: model.MediaSourceURL, URL: "data:image/png;base64,AA=="}}}}},
		{"insecure-url", model.Message{Role: model.RoleUser, Content: []model.ContentPart{{
			Kind: model.ContentImage, MediaType: "image/png", Source: &model.MediaSource{Kind: model.MediaSourceURL, URL: "http://media.example.test/image.png"}}}}},
		{"mixed-sources", model.Message{Role: model.RoleUser, Content: []model.ContentPart{{
			Kind: model.ContentImage, MediaType: "image/png", Source: &model.MediaSource{
				Kind: model.MediaSourceFileID, FileID: "f-1", URL: "https://media.example.test/x.png"}}}}},
		{"missing-media-type", model.Message{Role: model.RoleUser, Content: []model.ContentPart{{
			Kind: model.ContentImage, Source: &model.MediaSource{Kind: model.MediaSourceFileID, FileID: "f-1"}}}}},
		{"assistant-image", model.Message{Role: model.RoleAssistant, Content: []model.ContentPart{validImage}}},
		{"system-image", model.Message{Role: model.RoleSystem, Content: []model.ContentPart{validImage}}},
		{"tool-image", model.Message{Role: model.RoleTool, ToolCallID: "call-1", Content: []model.ContentPart{validImage}}},
		{"empty-text", model.Message{Role: model.RoleUser, Content: model.TextParts("")}},
		{"over-max-parts", model.Message{Role: model.RoleUser, Content: make([]model.ContentPart, 65)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !errors.Is(model.ValidateMessage(tt.message), model.ErrInvalidContent) {
				t.Fatalf("invalid/untrusted media was admitted: %+v", tt.message)
			}
		})
	}
	var legacy model.Message
	if json.Unmarshal([]byte("{"+chrQuote("role")+":"+chrQuote("user")+","+chrQuote("content")+":"+chrQuote("legacy string")+"}"), &legacy) == nil {
		t.Fatal("legacy string Content decoded into the hard-cut ordered blocks")
	}
}

func chrQuote(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}
