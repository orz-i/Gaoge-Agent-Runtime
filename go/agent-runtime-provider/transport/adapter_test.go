package transport

import (
	"testing"
)

func TestSupportsStreamingAdapter(t *testing.T) {
	if !SupportsStreamingAdapter(AdapterOpenAIImageGenerations) {
		t.Fatalf("expected image generations adapter to support upstream streaming")
	}
	if !SupportsStreamingAdapter(AdapterOpenAIImageEdits) {
		t.Fatalf("expected image edits adapter to support upstream streaming")
	}
	if !SupportsStreamingAdapter(AdapterOpenAIResponses) {
		t.Fatalf("expected responses adapter to support streaming")
	}
	if !SupportsStreamingAdapter(AdapterGoogleInteractions) {
		t.Fatalf("expected Google Interactions adapter to support streaming")
	}
	if !SupportsStreamingAdapter(AdapterVolcengineResponses) {
		t.Fatalf("expected Volcengine Responses adapter to support streaming")
	}
	if SupportsStreamingAdapter(AdapterVolcengineImage) {
		t.Fatalf("expected Volcengine image adapter to use non-streaming media flow")
	}
	if SupportsStreamingAdapter(AdapterVolcengineVideo) {
		t.Fatalf("expected Volcengine video adapter to use asynchronous polling")
	}
}

func TestGoogleInteractionsUsesNativeEndpointAndStatelessReplay(t *testing.T) {
	if got := DefaultEndpointForAdapter(AdapterGoogleInteractions); got != EndpointInteractions {
		t.Fatalf("expected Google Interactions endpoint, got %q", got)
	}
	if SupportsPreviousResponseID(AdapterGoogleInteractions) {
		t.Fatal("Google Interactions must remain stateless and must not advertise previous_interaction_id continuation")
	}
}

func TestSupportsImageGenerationStream(t *testing.T) {
	requireCondition(t, SupportsImageGenerationStream(AdapterOpenAIImageGenerations, "gpt-image-1"), "expected gpt-image models to support image generation streaming")
	requireCondition(t, SupportsImageGenerationStream(AdapterOpenAIImageGenerations, "gpt-image-2"), "expected gpt-image-2 to support image generation streaming")
	requireCondition(t, SupportsStreamingAdapter(AdapterGoogleImageGeneration), "expected google image generation adapter to support upstream streaming")
	requireCondition(t, SupportsImageGenerationStream(AdapterGoogleImageGeneration, "gemini-3-pro-image"), "expected google image generation adapter to support image generation streaming")
	requireCondition(t, !SupportsStreamingAdapter(AdapterXAIImage), "expected xAI image adapter to use non-streaming media flow")
	requireCondition(t, !SupportsStreamingAdapter(AdapterXAIImageEdits), "expected xAI image edits adapter to use non-streaming media flow")
	requireCondition(t, !SupportsImageGenerationStream(AdapterOpenAIImageGenerations, "dall-e-3"), "expected DALL-E models to remain non-streaming")
	requireCondition(t, !SupportsImageGenerationStream(AdapterOpenAIResponses, "gpt-image-1"), "expected non-image protocol to remain non-streaming for image generation")
	requireCondition(t, SupportsImageGenerationStream(AdapterOpenAIImageEdits, "gpt-image-1"), "expected gpt-image edits to support image edit streaming")
	requireCondition(t, SupportsImageGenerationStream(AdapterOpenAIImageEdits, "gpt-image-2"), "expected gpt-image-2 edits to support image edit streaming")
}

func TestImageAdapterCapabilities(t *testing.T) {
	if !IsImageGenerationAdapter(AdapterGoogleImageGeneration) {
		t.Fatalf("expected google image protocol to support image generation")
	}
	if !IsImageEditAdapter(AdapterGoogleImageGeneration) {
		t.Fatalf("expected google image protocol to support image editing")
	}
	if !IsImageGenerationAdapter(AdapterXAIImage) {
		t.Fatalf("expected xAI image protocol to support image generation")
	}
	if IsImageEditAdapter(AdapterXAIImage) {
		t.Fatalf("expected xAI image protocol to stay generation-only")
	}
	if !IsImageEditAdapter(AdapterXAIImageEdits) {
		t.Fatalf("expected xAI image edits protocol to support image editing")
	}
	if !IsImageGenerationAdapter(AdapterVolcengineImage) || !IsImageEditAdapter(AdapterVolcengineImage) {
		t.Fatalf("expected Volcengine image protocol to support generation and editing")
	}
	if !IsVideoGenerationAdapter(AdapterVolcengineVideo) {
		t.Fatalf("expected Volcengine video protocol to support video generation")
	}
	if got := DefaultEndpointForAdapter(AdapterVolcengineVideo); got != EndpointContentGenerationTasks {
		t.Fatalf("expected Volcengine video endpoint, got %q", got)
	}
}
