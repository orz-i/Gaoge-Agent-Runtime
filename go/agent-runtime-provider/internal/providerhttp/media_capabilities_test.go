package providerhttp

import (
	"errors"
	"testing"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

func TestMediaCapabilityMatrixIsPositiveAndExact(t *testing.T) {
	image := model.ContentPart{Kind: model.ContentImage, MediaType: "image/png", Source: &model.MediaSource{Kind: model.MediaSourceURL, URL: "https://example.test/image.png"}}
	pdf := model.ContentPart{Kind: model.ContentFile, MediaType: "application/pdf", Source: &model.MediaSource{Kind: model.MediaSourceFileID, FileID: "file-1"}}
	audio := model.ContentPart{Kind: model.ContentAudio, MediaType: "audio/mpeg", Source: &model.MediaSource{Kind: model.MediaSourceFileID, FileID: "audio-1"}}
	for _, tc := range []struct {
		name    string
		profile model.Capabilities
		parts   []model.ContentPart
		allow   bool
	}{
		{"text-default", model.Capabilities{}, model.TextParts("hello"), true},
		{"empty-default", model.Capabilities{}, nil, true},
		{"no-image-grant", model.Capabilities{}, []model.ContentPart{image}, false},
		{"no-pdf-grant", model.Capabilities{}, []model.ContentPart{pdf}, false},
		{"only-image", model.Capabilities{SupportsImageInput: true}, []model.ContentPart{image}, true},
		{"only-pdf", model.Capabilities{SupportsPDFInput: true}, []model.ContentPart{pdf}, true},
		{"image-not-pdf", model.Capabilities{SupportsImageInput: true}, []model.ContentPart{image, pdf}, false},
		{"pdf-not-image", model.Capabilities{SupportsPDFInput: true}, []model.ContentPart{image, pdf}, false},
		{"both", model.Capabilities{SupportsImageInput: true, SupportsPDFInput: true}, []model.ContentPart{image, pdf}, true},
		{"audio-never-allowed", model.Capabilities{SupportsImageInput: true, SupportsPDFInput: true}, []model.ContentPart{audio}, false},
		{"future-unknown-kind", model.Capabilities{SupportsImageInput: true, SupportsPDFInput: true}, []model.ContentPart{{Kind: model.ContentPartKind("future-video")}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := RequireMediaCapabilities([]model.Message{{Role: model.RoleUser, Content: tc.parts}}, tc.profile)
			if (err == nil) != tc.allow || (err != nil && !errors.Is(err, model.ErrCapabilityUnavailable)) {
				t.Fatalf("positive grants must be checked for each block: err=%v allowed=%v", err, tc.allow)
			}
		})
	}
}
