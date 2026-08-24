package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	a2a "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-a2a"
)

const resubscribeFixtureDelay = 2 * time.Second

type scenarioAgent struct{}

func (scenarioAgent) Execute(ctx context.Context, request a2a.HostedRequest, sink a2a.HostedSink) error {
	messageID := request.MessageView.ID
	switch {
	case matchesTCKID(messageID, "message-response"):
		return sink(a2a.HostedEvent{
			Kind: a2a.HostedEventDirectMessage, MessageID: responseID(messageID), Text: "Direct message response",
		})
	case matchesTCKID(messageID, "input-required"):
		text := firstText(request.MessageView.Parts)
		return sink(a2a.HostedEvent{
			Kind: a2a.HostedEventStatus, Status: a2a.HostedStatusInputRequired,
			MessageID: statusMessageID(messageID, text), Text: text,
		})
	case matchesTCKID(messageID, "reject-task"):
		return sink(a2a.HostedEvent{Kind: a2a.HostedEventStatus, Status: a2a.HostedStatusRejected})
	case matchesTCKID(messageID, "artifact-text"):
		return emitArtifact(sink, request, []a2a.ContentPart{{Kind: a2a.ContentPartText, Text: "Generated text content"}})
	case matchesTCKID(messageID, "artifact-file-url"):
		return emitArtifact(sink, request, []a2a.ContentPart{{
			Kind: a2a.ContentPartURL, URL: "https://example.com/output.txt", Filename: "output.txt", MediaType: "text/plain",
		}})
	case matchesTCKID(messageID, "artifact-file"):
		return emitArtifact(sink, request, []a2a.ContentPart{{
			Kind: a2a.ContentPartRaw, Raw: []byte("TCK file content"), Filename: "output.txt", MediaType: "text/plain",
		}})
	case matchesTCKID(messageID, "artifact-data"):
		return emitArtifact(sink, request, []a2a.ContentPart{{
			Kind: a2a.ContentPartData, Data: json.RawMessage(`{"key":"value","count":42}`),
		}})
	case matchesTCKID(messageID, "stream-artifact-chunked"):
		return emitChunkedArtifact(sink, request)
	case matchesTCKID(messageID, "stream-001"), matchesTCKID(messageID, "stream-003"),
		matchesTCKID(messageID, "stream-ordering-001"), matchesTCKID(messageID, "stream-artifact-text"):
		return emitStreamTextArtifact(sink, request, streamText(messageID))
	case matchesTCKID(messageID, "stream-artifact-file"):
		return emitStreamFileArtifact(sink, request)
	case matchesTCKID(messageID, "stream-002"):
		return sink(a2a.HostedEvent{Kind: a2a.HostedEventStatus, Status: a2a.HostedStatusCompleted})
	case strings.HasPrefix(messageID, "test-resubscribe-message-id"):
		return emitDelayedCompletion(ctx, sink)
	default:
		return sink(a2a.HostedEvent{Kind: a2a.HostedEventMessage, MessageID: responseID(messageID), Text: "Hello from TCK"})
	}
}

func (scenarioAgent) Cancel(context.Context, a2a.HostedCancelRequest) error { return nil }

func matchesTCKID(messageID, scenario string) bool {
	return strings.HasPrefix(messageID, "tck-"+scenario+"-")
}

func responseID(messageID string) string { return messageID + "-response" }

func statusMessageID(messageID, text string) string {
	digest := sha256.Sum256([]byte(text))
	return fmt.Sprintf("%s-status-%x", responseID(messageID), digest[:6])
}

func firstText(parts []a2a.ContentPart) string {
	for _, part := range parts {
		if text := strings.TrimSpace(part.Text); text != "" {
			return text
		}
	}
	return "More input is required"
}

func artifactID(request a2a.HostedRequest) string { return request.TaskID + "-artifact" }

func emitArtifact(sink a2a.HostedSink, request a2a.HostedRequest, parts []a2a.ContentPart) error {
	if err := sink(a2a.HostedEvent{
		Kind: a2a.HostedEventArtifact, ArtifactID: artifactID(request), Name: "TCK artifact",
		Parts: parts, LastChunk: true,
	}); err != nil {
		return err
	}
	return sink(a2a.HostedEvent{Kind: a2a.HostedEventStatus, Status: a2a.HostedStatusCompleted})
}

func emitChunkedArtifact(sink a2a.HostedSink, request a2a.HostedRequest) error {
	id := artifactID(request)
	if err := sink(a2a.HostedEvent{
		Kind: a2a.HostedEventArtifact, ArtifactID: id, Name: "TCK chunked artifact", Text: "chunk-1 ",
	}); err != nil {
		return err
	}
	if err := sink(a2a.HostedEvent{
		Kind: a2a.HostedEventArtifact, ArtifactID: id, Append: true, LastChunk: true, Text: "chunk-2",
	}); err != nil {
		return err
	}
	return sink(a2a.HostedEvent{Kind: a2a.HostedEventStatus, Status: a2a.HostedStatusCompleted})
}

func emitStreamTextArtifact(sink a2a.HostedSink, request a2a.HostedRequest, text string) error {
	return emitArtifact(sink, request, []a2a.ContentPart{{Kind: a2a.ContentPartText, Text: text}})
}

func emitStreamFileArtifact(sink a2a.HostedSink, request a2a.HostedRequest) error {
	return emitArtifact(sink, request, []a2a.ContentPart{{
		Kind: a2a.ContentPartRaw, Raw: []byte("TCK streamed file"), Filename: "output.txt", MediaType: "text/plain",
	}})
}

func streamText(messageID string) string {
	switch {
	case matchesTCKID(messageID, "stream-001"):
		return "Stream hello from TCK"
	case matchesTCKID(messageID, "stream-003"):
		return "Stream task lifecycle"
	case matchesTCKID(messageID, "stream-ordering-001"):
		return "Ordered output"
	default:
		return "Streamed text content"
	}
}

func emitDelayedCompletion(ctx context.Context, sink a2a.HostedSink) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(resubscribeFixtureDelay):
		return sink(a2a.HostedEvent{Kind: a2a.HostedEventStatus, Status: a2a.HostedStatusCompleted})
	}
}

var _ a2a.HostedAgent = scenarioAgent{}
