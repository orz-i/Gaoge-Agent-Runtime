package model_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"unicode/utf8"

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

func TestMessageRejectsInvalidUTF8TextWithoutChangingValidUnicode(t *testing.T) {
	invalid := []struct {
		name string
		text string
	}{
		{"isolated-continuation", "start \x80"},
		{"invalid-leading-byte", "start \xff"},
		{"overlong-encoding", "start \xc0\xaf"},
		{"truncated-multibyte", "start \xe2\x82"},
		{"surrogate-encoding", "start \xed\xa0\x80"},
		{"out-of-range-codepoint", "start \xf4\x90\x80\x80"},
	}
	for _, tc := range invalid {
		for _, role := range []model.Role{model.RoleUser, model.RoleAssistant, model.RoleSystem, model.RoleTool} {
			t.Run(tc.name+"/"+string(role), func(t *testing.T) {
				message := model.Message{Role: role, Content: model.TextParts(tc.text)}
				if role == model.RoleTool {
					message.ToolCallID = "call"
				}
				if err := model.ValidateMessage(message); !errors.Is(err, model.ErrInvalidContent) {
					t.Fatalf("invalid UTF-8 %x passed model contract: %v", []byte(tc.text), err)
				}
			})
		}
	}
	for _, text := range []string{"你好，世界", "é", "hello\n世界", "emoji 🧩"} {
		if err := model.ValidateMessage(model.Message{Role: model.RoleUser, Content: model.TextParts(text)}); err != nil {
			t.Fatalf("valid UTF-8 text %q denied: %v", text, err)
		}
	}
}

// A malformed byte sequence must never be accepted and then silently
// normalized by encoding/json when the immutable request is sealed.
func FuzzModelTextMessageCanonicalJSON(f *testing.F) {
	for _, sample := range [][]byte{
		[]byte("normal text"), []byte("你好 🧩"), []byte("é\nworld"),
		{0xff}, {0xfe}, {0xc0, 0xaf}, {0xed, 0xa0, 0x80}, {0xe2, 0x82},
		{0xf4, 0x90, 0x80, 0x80}, {'x', 0x00, 'y'}, {},
	} {
		f.Add(sample)
	}
	f.Fuzz(func(t *testing.T, payload []byte) {
		if len(payload) > 2048 {
			t.Skip()
		}
		message := model.Message{Role: model.RoleUser, Content: model.TextParts(string(payload))}
		err := model.ValidateMessage(message)
		if !utf8.Valid(payload) && err == nil {
			t.Fatalf("invalid bytes accepted: %x", payload)
		}
		if err != nil {
			return
		}
		encoded, err := json.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		var decoded model.Message
		if err := json.Unmarshal(encoded, &decoded); err != nil || !reflect.DeepEqual(message, decoded) {
			t.Fatalf("accepted message changed during JSON round trip: input=%x decoded=%+v err=%v", payload, decoded, err)
		}
	})
}

func chrQuote(s string) string {
	raw, _ := json.Marshal(s)
	return string(raw)
}
