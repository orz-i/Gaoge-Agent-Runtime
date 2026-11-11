package transport

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"strings"
	"testing"
)

func mediaFixtureImageEditAdapters() []mediaFixtureImageAdapterCase {
	return []mediaFixtureImageAdapterCase{
		{name: "openai", protocol: AdapterOpenAIImageEdits, model: mediaFixtureOpenAIModel, path: mediaFixtureEditsPath},
		{name: "openai_stream", protocol: AdapterOpenAIImageEdits, model: mediaFixtureOpenAIModel, path: mediaFixtureEditsPath, stream: true},
		{name: "gemini", protocol: AdapterGoogleImageGeneration, model: mediaFixtureGeminiModel, path: "/v1beta/models/gemini-2.5-flash-image:generateContent"},
		{name: "gemini_stream", protocol: AdapterGoogleImageGeneration, model: mediaFixtureGeminiModel, path: "/v1beta/models/gemini-2.5-flash-image:streamGenerateContent", stream: true},
		{name: "xai", protocol: AdapterXAIImageEdits, model: "grok-imagine-image", path: mediaFixtureEditsPath},
	}
}

func mediaFixtureMask() *ContentPart {
	return &ContentPart{
		Kind: ContentPartImage, MimeType: mediaFixturePNGMIME, FileName: "区域\"保留\".png",
		Data: []byte{0x89, 'P', 'N', 'G', 0, 0xff, '\n', '\r', 0x80, 9},
	}
}

func TestMediaAdapterImageEditsPreserveBodyReferenceOrderAndBytes(t *testing.T) {
	for _, adapter := range mediaFixtureImageEditAdapters() {
		t.Run(adapter.name, func(t *testing.T) {
			for _, count := range []int{1, 3} {
				name := "single_reference"
				if count > 1 {
					name = "ordered_references"
				}
				t.Run(name, func(t *testing.T) {
					runMediaAdapterImageEdit(t, adapter, mediaFixtureReferences()[:count])
				})
			}
		})
	}
}

func runMediaAdapterImageEdit(t *testing.T, adapter mediaFixtureImageAdapterCase, images []ContentPart) {
	t.Helper()
	client, capture := newMediaAdapterHTTPClient(t, mediaFixtureImageResponse(t, adapter.protocol, mediaFixtureRevised, adapter.stream))
	input := mediaFixtureInput(images)
	if adapter.protocol == AdapterOpenAIImageEdits {
		input.ImageEditMask = mediaFixtureMask()
	}
	before := mediaFixtureJSON(t, input)
	output := generateMediaAdapterImage(t, client, adapter, input)
	request := requireMediaAdapterSingleRequest(t, capture, adapter.path)
	if adapter.protocol == AdapterOpenAIImageEdits {
		requireMediaAdapterMultipart(t, request, images, input.ImageEditMask)
	} else {
		requireMediaAdapterImageJSON(t, request, adapter.protocol, mediaFixtureReviewed, images)
	}
	requireMediaAdapterRevisedImage(t, output, mediaFixtureRevised)
	requireMediaAdapterInputUnchanged(t, before, input)
}

type mediaFixtureMultipartFile struct {
	field    string
	name     string
	mimeType string
	data     []byte
}

func readMediaAdapterMultipart(t *testing.T, request mediaFixtureHTTPRequest) (map[string]string, []mediaFixtureMultipartFile) {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(request.contentType)
	requireNoError(t, err, "parse submitted multipart content type: %v")
	requireEqual(t, mediaType, "multipart/form-data", "submitted content type = %q", mediaType)
	reader := multipart.NewReader(bytes.NewReader(request.body), params["boundary"])
	fields := map[string]string{}
	var files []mediaFixtureMultipartFile
	for {
		part, readErr := reader.NextPart()
		if readErr == io.EOF {
			break
		}
		requireNoError(t, readErr, "read submitted multipart part: %v")
		data, dataErr := io.ReadAll(part)
		requireNoError(t, dataErr, "read submitted multipart bytes: %v")
		requireNoError(t, part.Close(), "close submitted multipart part: %v")
		if part.FileName() == "" {
			if _, duplicate := fields[part.FormName()]; duplicate {
				t.Fatalf("duplicate multipart field %q", part.FormName())
			}
			fields[part.FormName()] = string(data)
			continue
		}
		files = append(files, mediaFixtureMultipartFile{
			field: part.FormName(), name: part.FileName(),
			mimeType: part.Header.Get("Content-Type"), data: data,
		})
	}
	return fields, files
}

func requireMediaAdapterMultipart(t *testing.T, request mediaFixtureHTTPRequest, images []ContentPart, mask *ContentPart) {
	t.Helper()
	fields, files := readMediaAdapterMultipart(t, request)
	requireEqual(t, fields["prompt"], mediaFixtureReviewed,
		"submitted multipart prompt changed\ngot: %q\nwant: %q", fields["prompt"], mediaFixtureReviewed)
	if len(files) != len(images)+1 {
		t.Fatalf("submitted multipart files = %d, want %d references and one mask", len(files), len(images))
	}
	for index, image := range images {
		requireMediaAdapterMultipartFile(t, files[index], "image[]", image)
	}
	requireMediaAdapterMultipartFile(t, files[len(images)], "mask", *mask)
}

func requireMediaAdapterMultipartFile(t *testing.T, actual mediaFixtureMultipartFile, field string, expected ContentPart) {
	t.Helper()
	if actual.field != field || actual.name != expected.FileName || actual.mimeType != expected.MimeType {
		t.Fatalf("submitted multipart metadata = %#v, want %s/%s/%s", actual, field, expected.FileName, expected.MimeType)
	}
	if !bytes.Equal(actual.data, expected.Data) {
		t.Fatalf("%s bytes changed or references reordered: got %x, want %x", field, actual.data, expected.Data)
	}
}

// These adapters have no mask encoding. Silently accepting the input would
// dispatch a different edit than the caller approved; unsupported edits must
// instead fail before any provider request, including streaming and empty masks.
func TestMediaAdapterImageEditsRejectUnsupportedMaskBeforeDispatch(t *testing.T) {
	for _, adapter := range mediaFixtureImageEditAdapters() {
		if adapter.protocol == AdapterOpenAIImageEdits {
			continue
		}
		t.Run(adapter.name, func(t *testing.T) {
			runMediaAdapterUnsupportedMaskCases(t, adapter, mediaFixtureReferences()[:1])
		})
	}
}

func TestMediaAdapterImageGenerationRejectsUnsupportedMaskBeforeDispatch(t *testing.T) {
	for _, adapter := range mediaFixtureImageAdapters() {
		if adapter.protocol == AdapterOpenAIImageGenerations {
			continue
		}
		t.Run(adapter.name, func(t *testing.T) {
			runMediaAdapterUnsupportedMaskCases(t, adapter, nil)
		})
	}
}

func runMediaAdapterUnsupportedMaskCases(t *testing.T, adapter mediaFixtureImageAdapterCase, images []ContentPart) {
	t.Helper()
	for _, test := range []struct {
		name string
		mask *ContentPart
	}{
		{name: "binary_mask", mask: mediaFixtureMask()},
		{name: "empty_mask", mask: &ContentPart{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := mediaFixtureInput(images)
			input.ImageEditMask = test.mask
			requireMediaAdapterMaskRejected(t, adapter, input)
		})
	}
}

func requireMediaAdapterMaskRejected(t *testing.T, adapter mediaFixtureImageAdapterCase, input GenerateInput) {
	t.Helper()
	client, capture := newMediaAdapterHTTPClient(t)
	before := mediaFixtureJSON(t, input)
	route := mediaFixtureRoute(adapter.protocol, adapter.model)
	var err error
	if adapter.stream {
		_, err = client.GenerateStream(t.Context(), route, input, nil)
	} else {
		_, err = client.Generate(t.Context(), route, input)
	}
	if !errors.Is(err, ErrUnsupportedImageMask) || !errors.Is(err, ErrUnsupportedAdapter) {
		t.Fatalf("mask error = %v, want explicit incompatible-mask and unsupported-adapter classifications", err)
	}
	if !strings.Contains(err.Error(), adapter.protocol) {
		t.Errorf("mask error must identify the incompatible adapter: %v", err)
	}
	if !errors.Is(err, ErrUnsupportedAdapter) {
		t.Errorf("mask incompatibility lost its non-internal gateway classification: %v", err)
	}
	requireEqual(t, len(capture.requests), 0, "unsupported mask caused %d provider requests", len(capture.requests))
	requireMediaAdapterInputUnchanged(t, before, input)
}
