package agentruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"
	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	eventLLMRouteSelected       = "llm.route_selected"
	llmRouteRequestIDPayloadKey = "requestID"
)

func (s *Engine) textRunStartResult(ctx context.Context, actor model.ActorRef, run *model.Run) (*TextRunStartResult, error) {
	if run == nil {
		return nil, ErrNotFound
	}
	if run.Actor != actor {
		return nil, ErrNotFound
	}
	result := &TextRunStartResult{Run: *run, Projection: TurnProjection{Input: run.InputProjection, Output: run.OutputProjection}}
	steps, err := s.repo.ListRunSteps(ctx, run.RunID)
	if err != nil {
		return nil, err
	}
	if len(steps) > 0 {
		result.Step = steps[0]
	}
	return result, nil
}

func (s *Engine) appendRunEvent(ctx context.Context, run *model.Run, eventType, stepID, summary string, payload map[string]interface{}, projection *model.ProjectionRef) error {
	event := newRunEvent(*run, eventType, stepID, summary, payload, projection)
	saved, created, err := s.repo.AppendRunEvent(ctx, &event)
	if err != nil {
		return err
	}
	if created {
		s.PublishRunNotification(run.RunID, runEventEnvelope(saved))
	}
	return nil
}

func newRunEvent(run model.Run, eventType, stepID, summary string, payload map[string]interface{}, projection *model.ProjectionRef) model.Event {
	data, err := json.Marshal(payload)
	if err != nil {
		data = []byte(`{"error":"event_payload_encoding_failed"}`)
	}
	event := model.Event{Actor: run.Actor, Thread: run.Thread, RunID: run.RunID, EventID: "evt_" + strings.ReplaceAll(uuid.NewString(), "-", ""), EventType: eventType, SchemaVersion: 1, StepID: stepID, Visibility: valueUserDD885A59, Status: runEventStatus(eventType), Summary: truncateRunEventSummary(summary), PayloadJSON: string(data), StartedAt: time.Now()}
	if projection != nil {
		event.Projection = *projection
	}
	return event
}

func (s *Engine) recordRunLLMRouteSelected(ctx context.Context, run model.Run, stepID, phase string, route *LLMRoute, generationRequestID string) error {
	generationRequestID = strings.TrimSpace(generationRequestID)
	if route == nil || generationRequestID == "" {
		return ErrInvalidInput
	}
	payload := map[string]interface{}{
		llmRouteRequestIDPayloadKey: generationRequestID,
		"phase":                     strings.TrimSpace(phase),
		"platformModelRef":          route.PlatformModelRef,
		"platformModelName":         strings.TrimSpace(route.PlatformModelName),
		"routeRef":                  route.Ref,
		"upstreamModelRef":          route.UpstreamModelRef,
		"upstreamRef":               route.UpstreamRef,
		"upstreamName":              strings.TrimSpace(route.UpstreamName),
		"bindingCode":               strings.TrimSpace(route.BindingCode),
		"upstreamModel":             strings.TrimSpace(route.UpstreamModel),
		"protocol":                  strings.TrimSpace(route.Protocol),
		"endpoint":                  strings.TrimSpace(routeEndpoint(route)),
		"modelVendor":               strings.TrimSpace(route.ModelVendor),
		"modelIcon":                 strings.TrimSpace(route.ModelIcon),
	}
	event := newRunEvent(run, eventLLMRouteSelected, stepID, phase, payload, nil)
	event.EventID = llmRouteSelectedEventID(run.RunID, generationRequestID)
	saved, created, err := s.repo.AppendRunEvent(ctx, &event)
	if err != nil {
		return err
	}
	if created {
		s.PublishRunNotification(run.RunID, runEventEnvelope(saved))
	}
	return nil
}

func llmRouteSelectedEventID(runID, generationRequestID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(runID) + "\x00" + strings.TrimSpace(generationRequestID)))
	return "evt_llm_route_" + hex.EncodeToString(sum[:16])
}

func truncateRunEventSummary(value string) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) > 255 {
		return string(runes[:255])
	}
	return string(runes)
}

func (s *Engine) appendRunEvents(ctx context.Context, runID string, events []model.Event) error {
	saved, err := s.repo.AppendRunEvents(ctx, events)
	if err != nil {
		return err
	}
	s.publishRunEvents(runID, saved)
	return nil
}

func (s *Engine) publishRunEvents(runID string, events []model.Event) {
	for i := range events {
		event := events[i]
		s.PublishRunNotification(runID, runEventEnvelope(&event))
	}
}

func runEventEnvelope(e *model.Event) map[string]interface{} {
	var payload map[string]interface{}
	_ = json.Unmarshal([]byte(e.PayloadJSON), &payload)
	if payload == nil {
		payload = map[string]interface{}{}
	}
	appendPublicRunEventFields(payload, e.Summary, e.Status, e.ToolCallID, e.ToolName)
	durable := map[string]interface{}{"schemaVersion": 1, "eventID": e.EventID, "runID": e.RunID, valueActorRefKey: map[string]string{"tenantID": e.Actor.TenantID, "id": e.Actor.ActorID}, valueThreadRefKey: map[string]string{"kind": e.Thread.Kind, "id": e.Thread.ID}, "seq": e.Seq, valueType106D7553: e.EventType, valueStepID549B95DB: e.StepID, "parentEventID": e.ParentEventID, "timestamp": e.StartedAt.UTC().Format(time.RFC3339Nano), "payload": payload}
	return map[string]interface{}{valueType106D7553: "run_event", "event": durable}
}

func appendPublicRunEventFields(payload map[string]interface{}, summary, status, toolCallID, toolName string) {
	for key, value := range map[string]string{"summary": summary, "status": status, "toolCallID": toolCallID, "toolName": toolName} {
		if _, exists := payload[key]; exists || strings.TrimSpace(value) == "" {
			continue
		}
		payload[key] = strings.TrimSpace(value)
	}
}

func runEventStatus(kind string) string {
	switch kind {
	case valueRunPreparingA8E38F48:
		return model.RunStatusPreparing
	case valueRunWaitingInputF2C37C0A, "step.waiting_input":
		return model.RunStatusWaitingInput
	case "run.waiting_handoff", "step.waiting_handoff":
		return model.RunStatusWaitingHandoff
	case "step.created":
		return model.RunStatusQueued
	case "step.skipped":
		return "skipped"
	}
	if strings.HasSuffix(kind, ".failed") {
		return model.RunStatusFailed
	}
	if strings.HasSuffix(kind, ".completed") {
		return model.RunStatusCompleted
	}
	if strings.HasSuffix(kind, ".cancelled") {
		return model.RunStatusCancelled
	}
	if strings.HasSuffix(kind, ".suspended") {
		return model.RunStatusSuspended
	}
	return model.RunStatusRunning
}

func truncateRunTitle(v string) string {
	r := []rune(strings.TrimSpace(v))
	if len(r) > 80 {
		return string(r[:80])
	}
	return string(r)
}

func mustRunJSON(value interface{}) string {
	data, err := json.Marshal(value)
	if err != nil {
		return `{"error":"json_encoding_failed"}`
	}
	return string(data)
}

func (s *Engine) GetTextRunDetail(ctx context.Context, actor model.ActorRef, runID string) (*TextRunDetail, error) {
	run, err := s.repo.GetRun(ctx, actor, runID)
	if err != nil {
		return nil, err
	}
	steps, err := s.repo.ListRunSteps(ctx, runID)
	if err != nil {
		return nil, err
	}
	detail := &TextRunDetail{Run: *run, Steps: steps, Projection: TurnProjection{Input: run.InputProjection, Output: run.OutputProjection}}
	var effective effectiveTextRunConfig
	if json.Unmarshal([]byte(run.RunConfigSnapshotJSON), &effective) == nil {
		protocol := s.resolveProviderProtocolForSummary(ctx, *run, effective.PlatformModelName)
		detail.Config = summarizeTextRunConfig(effective, protocol)
	}
	if snapshot, snapshotErr := s.repo.GetRunContextSnapshot(ctx, actor, runID); snapshotErr == nil {
		detail.Context = &TextRunContextSummary{SnapshotID: snapshot.SnapshotID, SemanticVersion: snapshot.SchemaVersion, ContentHash: snapshot.ContentHash, FileCount: snapshot.FileCount, RAGCount: snapshot.RAGCount, SkillCount: snapshot.SkillCount, MemoryCount: snapshot.MemoryCount, OutputCount: snapshot.OutputCount, EvidenceCount: snapshot.EvidenceCount, RetrievalFallbackCount: snapshot.RetrievalFallbackCount, SkippedCount: snapshot.SkippedCount, CompiledAt: snapshot.CreatedAt}
	}
	return detail, nil
}

func (s *Engine) ListRunEventsAfter(ctx context.Context, actor model.ActorRef, runID string, after int64) ([]model.Event, error) {
	return s.repo.ListRunEventsAfter(ctx, actor, runID, after, 500)
}

func (s *Engine) GetRunCursor(ctx context.Context, actor model.ActorRef, runID string) (*model.RunCursor, error) {
	return s.repo.GetRunCursor(ctx, actor, runID)
}

func (s *Engine) ListRunEventHistory(ctx context.Context, actor model.ActorRef, runID string, beforeSeq int64, limit int) (*RunEventHistoryPage, error) {
	if _, err := s.repo.GetRunCursor(ctx, actor, runID); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 300
	} else if limit > 500 {
		limit = 500
	}
	items, err := s.repo.ListRunEventsBefore(ctx, actor, runID, beforeSeq, limit+1)
	if err != nil {
		return nil, err
	}
	return buildRunEventHistoryPage(items, limit), nil
}

func buildRunEventHistoryPage(items []model.Event, limit int) *RunEventHistoryPage {
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	for left, right := 0, len(items)-1; left < right; left, right = left+1, right-1 {
		items[left], items[right] = items[right], items[left]
	}
	page := &RunEventHistoryPage{Results: items, HasMore: hasMore}
	if hasMore && len(items) > 0 {
		page.NextBeforeSeq = items[0].Seq
	}
	return page
}

func (s *Engine) GetRunEvent(ctx context.Context, actor model.ActorRef, runID, eventID string) (*model.Event, error) {
	return s.repo.GetRunEvent(ctx, actor, runID, eventID)
}
