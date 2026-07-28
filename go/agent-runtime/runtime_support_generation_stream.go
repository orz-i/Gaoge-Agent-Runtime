// Package agentruntime owns Agent Runtime use cases and policy.
package agentruntime

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	valueCode1D835B3A      = "code"
	valueCompleted71CA01B8 = "completed"
	valueInputBF074BCD     = "input"
	valueToolCallsA0D28F76 = "tool_calls"
	valueType84B07547      = "type"
)

type GenerationLeaseState string

const (
	GenerationLeaseActive  GenerationLeaseState = "active"
	GenerationLeaseExpired GenerationLeaseState = "expired"
	GenerationLeaseUnknown GenerationLeaseState = "unknown"
)

var errGenerationLeaseStoreUnavailable = errors.New("generation lease store is unavailable")

const (
	generationStreamRetention        = 15 * time.Minute
	generationStreamActiveTTL        = 2 * time.Hour
	generationStreamLeaseTTL         = 30 * time.Second
	generationStreamLeaseRefresh     = 10 * time.Second
	generationStreamMaxEvents        = 1024
	generationStreamSubscriberBuffer = 128
	generationStreamReadBlock        = 5 * time.Second
	generationStreamMaxPayloadBytes  = 128 * 1024
)

type generationStreamOptions struct {
	Retention        time.Duration
	ActiveTTL        time.Duration
	LeaseTTL         time.Duration
	LeaseRefresh     time.Duration
	MaxEvents        int
	SubscriberBuffer int
}

func defaultGenerationStreamOptions() generationStreamOptions {
	return generationStreamOptions{
		Retention:        generationStreamRetention,
		ActiveTTL:        generationStreamActiveTTL,
		LeaseTTL:         generationStreamLeaseTTL,
		LeaseRefresh:     generationStreamLeaseRefresh,
		MaxEvents:        generationStreamMaxEvents,
		SubscriberBuffer: generationStreamSubscriberBuffer,
	}
}

// EnsureRunID 规范化客户端 run ID；为空时生成新的公开 ID。
func EnsureRunID(raw string) string {
	runID := normalizeRunID(raw)
	if runID != "" {
		return runID
	}
	return "run_" + normalizePublicID(uuid.NewString())
}

// CancelRun 取消用户显式停止的流式生成；浏览器刷新不会走这个路径。
func (s *Engine) CancelRun(ctx context.Context, actor model.ActorRef, runID string) (bool, error) {
	normalizedRunID := normalizeRunID(runID)
	if s != nil && s.repo != nil {
		if run, err := s.repo.GetRun(ctx, actor, normalizedRunID); err == nil && run.RuntimeKind == model.RuntimeKindWorkflow {
			return s.cancelWorkflowRun(ctx, *run)
		}
	}
	_, found, handled, err := s.cancelDurableRunIfTerminalOrWaiting(ctx, actor, normalizedRunID)
	if err != nil || handled {
		return handled, err
	}
	if found {
		canceled, storeErr := s.generationStreams.cancelOwned(ctx, actor, normalizedRunID)
		if storeErr != nil {
			if canceled {
				s.logger.Warn("text_run_generation_cancel_store_degraded", String("run_id", normalizedRunID), String("actor_id", actor.ActorID), Error(storeErr))
				return true, nil
			}
			return false, ErrRunCancelUnavailable
		}
		return canceled, nil
	}
	canceled := s.generationStreams.cancel(ctx, actor, normalizedRunID)
	if !canceled || s == nil || s.repo == nil {
		return canceled, nil
	}
	return true, nil
}

func (s *Engine) cancelDurableRunIfTerminalOrWaiting(ctx context.Context, actor model.ActorRef, runID string) (model.Run, bool, bool, error) {
	if s == nil || s.repo == nil {
		return model.Run{}, false, false, nil
	}
	textRun := s.textRunForCancellation(ctx, actor, runID)
	if textRun == nil {
		return model.Run{}, false, false, nil
	}
	switch textRun.Status {
	case model.RunStatusCompleted, model.RunStatusFailed, model.RunStatusCancelled, model.RunStatusSuspended:
		return *textRun, true, true, nil
	case model.RunStatusWaitingInput, model.RunStatusWaitingHandoff:
		if err := s.cancelTextRun(ctx, *textRun, textRun.CurrentStepID, ErrRunCanceled.Error()); err != nil {
			return *textRun, true, false, err
		}
		s.FinishRunNotifications(textRun.RunID)
		return *textRun, true, true, nil
	default:
		return *textRun, true, false, nil
	}
}

func (s *Engine) textRunForCancellation(ctx context.Context, actor model.ActorRef, runID string) *model.Run {
	textRun, err := s.repo.GetRun(ctx, actor, runID)
	if err != nil {
		return nil
	}
	return textRun
}

// PublishRunNotification 发布生成流事件，并返回带 seq 的实际载荷。
func (s *Engine) PublishRunNotification(runID string, payload map[string]interface{}) map[string]interface{} {
	return s.generationStreams.publish(context.Background(), normalizeRunID(runID), payload)
}

// SubscribeRunNotifications 订阅用户所属 run 的生成流，返回可回放事件和后续事件通道。
func (s *Engine) SubscribeRunNotifications(
	ctx context.Context,
	actor model.ActorRef,
	runID string,
	afterSeq int64,
) ([]GenerationStreamEvent, <-chan GenerationStreamEvent, func(), bool) {
	return s.generationStreams.subscribe(ctx, actor, normalizeRunID(runID), afterSeq)
}

// FinishRunNotifications 标记生成流结束，并在短期恢复窗口后释放事件缓存。
func (s *Engine) FinishRunNotifications(runID string) {
	s.generationStreams.finish(context.Background(), normalizeRunID(runID))
	s.wakeRunQueue()
}

// HasActiveRunLease 判断该 run 是否仍持有活跃生成租约。
func (s *Engine) HasActiveRunLease(ctx context.Context, runID string) bool {
	if s == nil || s.generationStreams == nil {
		return false
	}
	return s.generationStreams.hasActive(ctx, normalizeRunID(runID))
}

// TextRunLeaseState returns fail-safe lease evidence for Text Runtime reconciliation.
func (s *Engine) TextRunLeaseState(ctx context.Context, runID string) (GenerationLeaseState, error) {
	if s == nil || s.generationStreams == nil {
		return GenerationLeaseUnknown, errGenerationLeaseStoreUnavailable
	}
	return s.generationStreams.leaseState(ctx, normalizeRunID(runID))
}

// MarkRunMessageInterrupted 将无法继续恢复的 pending 生成标记为中断。
func (s *Engine) isRunCanceled(ctx context.Context, runID string) bool {
	return s.generationStreams.isCanceled(ctx, normalizeRunID(runID))
}

func normalizeRunID(raw string) string {
	value := normalizePublicID(raw)
	if value == "" {
		return ""
	}
	if !strings.HasPrefix(value, "run_") {
		value = "run_" + value
	}
	if len(value) > 64 {
		return ""
	}
	return value
}

// GenerationStreamEvent 表示可恢复生成流中的一条有序事件。
type GenerationStreamEvent struct {
	ID      string
	Seq     int64
	Payload map[string]interface{}
}

type activeGeneration struct {
	actor           model.ActorRef
	cancel          context.CancelFunc
	leaseCancel     context.CancelFunc
	maxRuntimeTimer *time.Timer
}

type generationStreamRegistry struct {
	mu      sync.Mutex
	active  map[string]*activeGeneration
	store   GenerationStreamCacheRepository
	options generationStreamOptions
}

func newGenerationStreamRegistry(store GenerationStreamCacheRepository, options generationStreamOptions) *generationStreamRegistry {
	if options.Retention <= 0 {
		options.Retention = generationStreamRetention
	}
	if options.ActiveTTL <= 0 {
		options.ActiveTTL = generationStreamActiveTTL
	}
	if options.LeaseTTL <= 0 {
		options.LeaseTTL = generationStreamLeaseTTL
	}
	if options.LeaseRefresh <= 0 || options.LeaseRefresh >= options.LeaseTTL {
		options.LeaseRefresh = options.LeaseTTL / 3
	}
	if options.MaxEvents <= 0 {
		options.MaxEvents = generationStreamMaxEvents
	}
	if options.SubscriberBuffer <= 0 {
		options.SubscriberBuffer = generationStreamSubscriberBuffer
	}
	return &generationStreamRegistry{
		active:  map[string]*activeGeneration{},
		store:   store,
		options: options,
	}
}

func (r *generationStreamRegistry) register(ctx context.Context, runID string, actor model.ActorRef, cancel context.CancelFunc) {
	if runID == "" {
		if cancel != nil {
			cancel()
		}
		return
	}
	if r.store != nil {
		_ = r.store.RegisterGenerationStream(ctx, runID, actor, r.options.ActiveTTL)
	}

	var replaced *activeGeneration
	r.mu.Lock()
	replaced = r.active[runID]
	active := &activeGeneration{actor: actor, cancel: cancel}
	active.leaseCancel = r.startActiveLease(runID)
	r.active[runID] = active
	r.scheduleActiveExpiryLocked(runID, active)
	r.mu.Unlock()

	if replaced != nil {
		stopActiveGeneration(replaced)
		if replaced.cancel != nil {
			replaced.cancel()
		}
	}
}

func (r *generationStreamRegistry) cancel(ctx context.Context, actor model.ActorRef, runID string) bool {
	if runID == "" {
		return false
	}
	if !r.authorized(ctx, r.store, runID, actor) {
		return false
	}
	if r.store != nil {
		_ = r.store.RequestGenerationStreamCancel(ctx, runID, r.options.Retention)
	}

	active, ok := r.deleteActive(actor, runID)
	if ok {
		stopActiveGeneration(active)
	}
	if ok && active.cancel != nil {
		active.cancel()
	}
	r.clearActive(context.Background(), runID)
	return true
}

// cancelOwned is called only after the Text Run has been authorized against
// the durable database. It never depends on the stream store to cancel work
// that is active in this process; the store marker remains the cross-instance
// cancellation signal.
func (r *generationStreamRegistry) cancelOwned(ctx context.Context, actor model.ActorRef, runID string) (bool, error) {
	if r == nil || runID == "" || actor.ActorID == "" {
		return false, errGenerationLeaseStoreUnavailable
	}
	active, local := r.deleteActive(actor, runID)
	if local {
		stopActiveGeneration(active)
		if active.cancel != nil {
			active.cancel()
		}
	}
	if r.store == nil {
		return local, errGenerationLeaseStoreUnavailable
	}
	if err := r.store.RequestGenerationStreamCancel(ctx, runID, r.options.Retention); err != nil {
		return local, err
	}
	r.clearActive(context.Background(), runID)
	return true, nil
}

func (r *generationStreamRegistry) isCanceled(ctx context.Context, runID string) bool {
	if runID == "" {
		return false
	}
	if r.store != nil {
		if canceled, err := r.store.IsGenerationStreamCanceled(ctx, runID); err == nil && canceled {
			return true
		}
	}
	return false
}

func (r *generationStreamRegistry) publish(ctx context.Context, runID string, payload map[string]interface{}) map[string]interface{} {
	if runID == "" {
		return payload
	}
	r.touchActive(ctx, runID)
	actual := cloneStreamPayload(payload)
	persisted, sanitized := generationStreamPayloadForStore(actual)
	payloadJSON, err := marshalStreamPayload(persisted)
	if err != nil {
		return actual
	}
	record, err := r.append(ctx, r.store, runID, payloadJSON)
	if err == nil && record.Seq > 0 {
		actual["seq"] = record.Seq
		if sanitized {
			persisted["seq"] = record.Seq
		}
	}
	if shouldReturnSanitizedGenerationStreamPayload(actual, sanitized) {
		return persisted
	}
	return actual
}

func (r *generationStreamRegistry) append(ctx context.Context, store GenerationStreamCacheRepository, runID string, payloadJSON string) (GenerationStreamMessage, error) {
	if store == nil {
		return GenerationStreamMessage{}, nil
	}
	return store.AppendGenerationStreamEvent(ctx, runID, payloadJSON, int64(r.options.MaxEvents), r.options.ActiveTTL)
}

func (r *generationStreamRegistry) subscribe(
	ctx context.Context,
	actor model.ActorRef,
	runID string,
	afterSeq int64,
) ([]GenerationStreamEvent, <-chan GenerationStreamEvent, func(), bool) {
	if runID == "" {
		return nil, nil, nil, false
	}
	return r.subscribeStore(ctx, r.store, actor, runID, afterSeq)
}

func (r *generationStreamRegistry) subscribeStore(
	ctx context.Context,
	store GenerationStreamCacheRepository,
	actor model.ActorRef,
	runID string,
	afterSeq int64,
) ([]GenerationStreamEvent, <-chan GenerationStreamEvent, func(), bool) {
	if store == nil || !r.authorized(ctx, store, runID, actor) {
		return nil, nil, nil, false
	}
	retained, err := store.ListGenerationStreamEvents(ctx, runID, int64(r.options.MaxEvents))
	if err != nil {
		return nil, nil, nil, false
	}
	replay, cursor, terminal := retainedStreamEvents(retained, afterSeq)
	events := make(chan GenerationStreamEvent, r.options.SubscriberBuffer)
	if terminal {
		close(events)
		return replay, events, func() {}, true
	}

	readCtx, cancel := context.WithCancel(ctx)
	go r.readStoreEvents(readCtx, store, runID, cursor, afterSeq, events)
	return replay, events, cancel, true
}

func (r *generationStreamRegistry) readStoreEvents(
	ctx context.Context,
	store GenerationStreamCacheRepository,
	runID string,
	cursor string,
	afterSeq int64,
	out chan<- GenerationStreamEvent,
) {
	defer close(out)
	if strings.TrimSpace(cursor) == "" {
		cursor = "0-0"
	}
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		nextCursor, nextAfterSeq, done := r.readStoreEventBatch(ctx, store, runID, cursor, afterSeq, out)
		cursor = nextCursor
		afterSeq = nextAfterSeq
		if done {
			return
		}
	}
}

func (r *generationStreamRegistry) readStoreEventBatch(
	ctx context.Context,
	store GenerationStreamCacheRepository,
	runID string,
	cursor string,
	afterSeq int64,
	out chan<- GenerationStreamEvent,
) (string, int64, bool) {
	records, err := store.ReadGenerationStreamEvents(ctx, runID, cursor, generationStreamReadBlock, int64(r.options.SubscriberBuffer))
	if err != nil {
		return cursor, afterSeq, true
	}
	for _, record := range records {
		nextCursor, event, ok := nextStoreStreamEvent(record, cursor, afterSeq)
		cursor = nextCursor
		if !ok {
			continue
		}
		afterSeq = event.Seq
		if streamEventDeliveredOrDone(ctx, out, event) {
			return cursor, afterSeq, true
		}
	}
	return cursor, afterSeq, false
}

func nextStoreStreamEvent(record GenerationStreamMessage, cursor string, afterSeq int64) (string, GenerationStreamEvent, bool) {
	if strings.TrimSpace(record.ID) != "" {
		cursor = record.ID
	}
	if record.Seq <= afterSeq {
		return cursor, GenerationStreamEvent{}, false
	}
	event, ok := decodeStreamRecord(record)
	return cursor, event, ok
}

func streamEventDeliveredOrDone(ctx context.Context, out chan<- GenerationStreamEvent, event GenerationStreamEvent) bool {
	select {
	case <-ctx.Done():
		return true
	case out <- event:
	}
	return isTerminalStreamPayload(event.Payload)
}

func (r *generationStreamRegistry) finish(ctx context.Context, runID string) {
	if runID == "" {
		return
	}
	r.clearActive(ctx, runID)
	if r.store != nil {
		_ = r.store.ExpireGenerationStream(ctx, runID, r.options.Retention)
	}

	r.mu.Lock()
	active, ok := r.active[runID]
	if ok {
		delete(r.active, runID)
	}
	r.mu.Unlock()
	if ok {
		stopActiveGeneration(active)
	}
}

func (r *generationStreamRegistry) authorized(ctx context.Context, store GenerationStreamCacheRepository, runID string, actor model.ActorRef) bool {
	if store == nil || actor.ActorID == "" {
		return false
	}
	owner, ok, err := store.GetGenerationStreamOwner(ctx, runID)
	if err != nil || !ok {
		return false
	}
	return owner == actor
}

func (r *generationStreamRegistry) scheduleActiveExpiryLocked(runID string, active *activeGeneration) {
	if active.maxRuntimeTimer != nil {
		active.maxRuntimeTimer.Stop()
	}
	activeTTL := r.options.ActiveTTL
	if activeTTL <= 0 {
		activeTTL = generationStreamActiveTTL
	}
	active.maxRuntimeTimer = time.AfterFunc(activeTTL, func() {
		var cancel context.CancelFunc
		var leaseCancel context.CancelFunc
		r.mu.Lock()
		current, ok := r.active[runID]
		if ok && current == active && current.cancel != nil {
			delete(r.active, runID)
			cancel = current.cancel
			leaseCancel = current.leaseCancel
		}
		r.mu.Unlock()
		if leaseCancel != nil {
			leaseCancel()
		}
		r.clearActive(context.Background(), runID)
		if cancel != nil {
			cancel()
		}
	})
}

func (r *generationStreamRegistry) deleteActive(actor model.ActorRef, runID string) (*activeGeneration, bool) {
	if runID == "" {
		return nil, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	active, ok := r.active[runID]
	if !ok || active.actor != actor {
		return nil, false
	}
	delete(r.active, runID)
	return active, true
}

func (r *generationStreamRegistry) hasActive(ctx context.Context, runID string) bool {
	state, _ := r.leaseState(ctx, runID)
	return state == GenerationLeaseActive
}

func (r *generationStreamRegistry) leaseState(ctx context.Context, runID string) (GenerationLeaseState, error) {
	if runID == "" {
		return GenerationLeaseUnknown, errCategory003F85F311
	}
	r.mu.Lock()
	_, locallyActive := r.active[runID]
	r.mu.Unlock()
	if r.store == nil {
		if locallyActive {
			return GenerationLeaseActive, errGenerationLeaseStoreUnavailable
		}
		return GenerationLeaseUnknown, errGenerationLeaseStoreUnavailable
	}
	active, err := r.store.IsGenerationStreamActive(ctx, runID)
	if err != nil {
		if locallyActive {
			return GenerationLeaseActive, err
		}
		return GenerationLeaseUnknown, err
	}
	if active || locallyActive {
		return GenerationLeaseActive, nil
	}
	return GenerationLeaseExpired, nil
}

func (r *generationStreamRegistry) startActiveLease(runID string) context.CancelFunc {
	ctx, cancel := context.WithCancel(context.Background())
	r.touchActive(ctx, runID)
	go func() {
		ticker := time.NewTicker(r.options.LeaseRefresh)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				r.touchActive(ctx, runID)
			}
		}
	}()
	return cancel
}

func (r *generationStreamRegistry) touchActive(ctx context.Context, runID string) {
	if runID == "" {
		return
	}
	if r.store != nil {
		_ = r.store.TouchGenerationStreamActive(ctx, runID, r.options.LeaseTTL)
	}
}

func (r *generationStreamRegistry) clearActive(ctx context.Context, runID string) {
	if runID == "" {
		return
	}
	if r.store != nil {
		_ = r.store.ClearGenerationStreamActive(ctx, runID)
	}
}

func stopActiveGeneration(active *activeGeneration) {
	if active == nil {
		return
	}
	if active.maxRuntimeTimer != nil {
		active.maxRuntimeTimer.Stop()
		active.maxRuntimeTimer = nil
	}
	if active.leaseCancel != nil {
		active.leaseCancel()
		active.leaseCancel = nil
	}
}

func retainedStreamEvents(records []GenerationStreamMessage, afterSeq int64) ([]GenerationStreamEvent, string, bool) {
	replay := make([]GenerationStreamEvent, 0)
	cursor := "0-0"
	terminal := false
	for _, record := range records {
		if strings.TrimSpace(record.ID) != "" {
			cursor = record.ID
		}
		event, ok := decodeStreamRecord(record)
		if !ok {
			continue
		}
		if isTerminalStreamPayload(event.Payload) {
			terminal = true
		}
		if event.Seq > afterSeq {
			replay = append(replay, event)
		}
	}
	return replay, cursor, terminal
}

func decodeStreamRecord(record GenerationStreamMessage) (GenerationStreamEvent, bool) {
	payload := map[string]interface{}{}
	if err := json.Unmarshal([]byte(record.PayloadJSON), &payload); err != nil {
		return GenerationStreamEvent{}, false
	}
	seq := record.Seq
	if seq <= 0 {
		seq = int64FromPayload(payload["seq"])
	}
	if seq <= 0 {
		return GenerationStreamEvent{}, false
	}
	payload["seq"] = seq
	return GenerationStreamEvent{ID: record.ID, Seq: seq, Payload: payload}, true
}

func marshalStreamPayload(payload map[string]interface{}) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func generationStreamPayloadForStore(payload map[string]interface{}) (map[string]interface{}, bool) {
	if !isTraceUpdateStreamPayload(payload) {
		return payload, false
	}
	payloadJSON, err := marshalStreamPayload(payload)
	if err != nil || len(payloadJSON) <= generationStreamMaxPayloadBytes {
		return payload, false
	}
	sanitized := sanitizeGenerationStreamPayload(payload)
	payloadJSON, err = marshalStreamPayload(sanitized)
	if err != nil || len(payloadJSON) <= generationStreamMaxPayloadBytes {
		return sanitized, true
	}
	return compactOversizedGenerationStreamPayload(sanitized), true
}

func shouldReturnSanitizedGenerationStreamPayload(actual map[string]interface{}, sanitized bool) bool {
	return sanitized && isTraceUpdateStreamPayload(actual)
}

func isTraceUpdateStreamPayload(payload map[string]interface{}) bool {
	switch strings.TrimSpace(streamString(payload[valueType84B07547])) {
	case "process_update", "upstream_think_delta":
		return true
	default:
		return false
	}
}

func sanitizeGenerationStreamPayload(payload map[string]interface{}) map[string]interface{} {
	raw, err := json.Marshal(payload)
	if err != nil {
		next := cloneStreamPayload(payload)
		next["payloadTruncated"] = true
		return next
	}
	var normalized interface{}
	if err := json.Unmarshal(raw, &normalized); err != nil {
		next := cloneStreamPayload(payload)
		next["payloadTruncated"] = true
		return next
	}
	sanitized, _ := sanitizeGenerationStreamValue(normalized).(map[string]interface{})
	if sanitized == nil {
		sanitized = map[string]interface{}{}
	}
	sanitized["payloadTruncated"] = true
	return sanitized
}

func sanitizeGenerationStreamValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case map[string]interface{}:
		return sanitizeGenerationStreamMap(typed)
	case []interface{}:
		next := make([]interface{}, 0, len(typed))
		for _, item := range typed {
			next = append(next, sanitizeGenerationStreamValue(item))
		}
		return next
	default:
		return value
	}
}

func sanitizeGenerationStreamMap(typed map[string]interface{}) map[string]interface{} {
	next := make(map[string]interface{}, len(typed))
	for key, item := range typed {
		sanitizeGenerationStreamField(next, key, item)
	}
	return next
}

func sanitizeGenerationStreamField(next map[string]interface{}, key string, item interface{}) {
	if shouldDropStreamTraceField(key) {
		next[key+"_size"] = len(strings.TrimSpace(streamString(item)))
		next[key+"_truncated"] = true
		return
	}
	if isTracePayloadJSONField(key) {
		if sanitized := sanitizeStreamTracePayloadJSON(streamString(item)); sanitized != "" {
			next[key] = sanitized
		}
		return
	}
	if key == valueToolCallsA0D28F76 {
		next[key] = sanitizeStreamToolCalls(item)
		return
	}
	next[key] = sanitizeGenerationStreamValue(item)
}

func sanitizeStreamToolCalls(value interface{}) interface{} {
	items, ok := value.([]interface{})
	if !ok {
		return sanitizeGenerationStreamValue(value)
	}
	next := make([]interface{}, 0, len(items))
	for _, item := range items {
		record, ok := item.(map[string]interface{})
		if !ok {
			next = append(next, sanitizeGenerationStreamValue(item))
			continue
		}
		next = append(next, sanitizeGenerationStreamValue(record))
	}
	return next
}

func sanitizeStreamTracePayloadJSON(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	var payload interface{}
	if err := json.Unmarshal([]byte(value), &payload); err != nil {
		return ""
	}
	sanitized := sanitizeGenerationStreamValue(payload)
	data, err := json.Marshal(sanitized)
	if err != nil || string(data) == "{}" {
		return ""
	}
	return string(data)
}

func compactOversizedGenerationStreamPayload(payload map[string]interface{}) map[string]interface{} {
	eventType := strings.TrimSpace(streamString(payload[valueType84B07547]))
	next := map[string]interface{}{
		valueType84B07547:  eventType,
		"payloadTruncated": true,
	}
	for _, key := range []string{"status", "message", "errorCode", valueCode1D835B3A} {
		if value := strings.TrimSpace(streamString(payload[key])); value != "" {
			next[key] = compactSnippet(value, 512)
		}
	}
	if eventType == "" {
		next[valueType84B07547] = "stream_update"
	}
	return next
}

func shouldDropStreamTraceField(key string) bool {
	switch key {
	case valueInputBF074BCD, "input_detail", "output", "output_text", "output_detail":
		return true
	default:
		return false
	}
}

func isTracePayloadJSONField(key string) bool {
	return key == "payloadJSON" || key == "PayloadJSON" || key == "payloadJson"
}

func streamString(value interface{}) string {
	text, _ := value.(string)
	return text
}

func cloneStreamPayload(payload map[string]interface{}) map[string]interface{} {
	next := make(map[string]interface{}, len(payload)+1)
	for key, value := range payload {
		next[key] = value
	}
	return next
}

func isTerminalStreamPayload(payload map[string]interface{}) bool {
	eventType, _ := payload[valueType84B07547].(string)
	return eventType == valueCompleted71CA01B8 || eventType == "error"
}

func int64FromPayload(raw interface{}) int64 {
	switch value := raw.(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	case json.Number:
		n, _ := value.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		return n
	default:
		return 0
	}
}

var (
	errCategory003F85F311 = errors.New("generation lease run id is empty")
)
