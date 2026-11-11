package model_test

import (
	"encoding/json"

	"strings"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestHostedArtifactContentIsImmutableReference(t *testing.T) {
	original := model.Message{Role: model.RoleUser, CacheControl: &model.CacheControl{Type: "ephemeral"}, Content: []model.ContentPart{
		{Kind: model.ContentVideo, MediaType: "video/mp4", Name: "clip.mp4", CacheControl: &model.CacheControl{Type: "ephemeral", TTL: "5m"}, Source: &model.MediaSource{Kind: model.MediaSourceArtifact, ArtifactID: "tenant/immutable-asset", SHA256: strings.Repeat("a", 64), SizeBytes: 123}},
	}}
	if err := model.ValidateMessage(original); err != nil {
		t.Fatal(err)
	}
	cloned := model.CloneMessages([]model.Message{original})
	cloned[0].Content[0].Source.ArtifactID = "changed"
	cloned[0].Content[0].CacheControl.TTL = "1h"
	cloned[0].CacheControl.Type = "changed"
	if original.Content[0].Source.ArtifactID != "tenant/immutable-asset" || original.Content[0].CacheControl.TTL != "5m" || original.CacheControl.Type != "ephemeral" {
		t.Fatal("clone leaked mutable reference")
	}
	encoded, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var decoded model.Message
	if err = json.Unmarshal(encoded, &decoded); err != nil || model.ValidateMessage(decoded) != nil {
		t.Fatal("durable reference roundtrip", err)
	}
	for name, mutate := range map[string]func(*model.MediaSource){
		"missing digest":   func(s *model.MediaSource) { s.SHA256 = "" },
		"uppercase digest": func(s *model.MediaSource) { s.SHA256 = strings.Repeat("A", 64) },
		"zero size":        func(s *model.MediaSource) { s.SizeBytes = 0 },
		"ambiguous url":    func(s *model.MediaSource) { s.URL = "https://example.test/a" },
		"ambiguous file":   func(s *model.MediaSource) { s.FileID = "file-1" },
		"empty id":         func(s *model.MediaSource) { s.ArtifactID = "" },
		"untrimmed id":     func(s *model.MediaSource) { s.ArtifactID = " bad" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := model.CloneMessages([]model.Message{original})[0]
			mutate(bad.Content[0].Source)
			if model.ValidateMessage(bad) == nil {
				t.Fatal("accepted invalid artifact")
			}
		})
	}
}

func TestHostedContentNeverBroadensExistingRemoteMedia(t *testing.T) {
	for _, media := range []string{"application/json", "application/msword"} {
		part := model.ContentPart{Kind: model.ContentFile, MediaType: media, Source: &model.MediaSource{Kind: model.MediaSourceURL, URL: "https://example.test/file"}}
		if model.ValidateMessage(model.Message{Role: model.RoleUser, Content: []model.ContentPart{part}}) == nil {
			t.Fatal("remote file capability broadened")
		}
		part.Source = &model.MediaSource{Kind: model.MediaSourceArtifact, ArtifactID: "asset", SHA256: strings.Repeat("b", 64), SizeBytes: 12}
		if err := model.ValidateMessage(model.Message{Role: model.RoleUser, Content: []model.ContentPart{part}}); err != nil {
			t.Fatal(err)
		}
	}
	bad := model.Message{Role: model.RoleUser, Content: model.TextParts("hello"), CacheControl: &model.CacheControl{Type: "unsupported"}}
	if model.ValidateMessage(bad) == nil {
		t.Fatal("invalid cache accepted")
	}
}

func TestRequestBindingValidation(t *testing.T) {
	for _, value := range []string{" trailing", "line\nbreak", strings.Repeat("a", 513), string([]byte{255})} {
		for _, request := range []model.Request{{BindingRef: value}, {PromptCacheKey: value}, {PreviousResponseID: value}} {
			if model.ValidateRequestBinding(request) == nil {
				t.Fatal("invalid binding accepted")
			}
		}
	}
	if err := model.ValidateRequestBinding(model.Request{BindingRef: "route-snapshot", PromptCacheKey: "cache", PreviousResponseID: "response"}); err != nil {
		t.Fatal(err)
	}
}
