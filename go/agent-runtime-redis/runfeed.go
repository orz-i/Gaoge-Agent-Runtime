package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	goredis "github.com/go-redis/redis/v8"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/runfeed"
)

// RunFeedOptions configures the Redis Run Feed adapter.
type RunFeedOptions struct {
	KeyPrefix string
}

// RunFeedStore persists replayable Run Feed events in Redis.
type RunFeedStore struct {
	client goredis.UniversalClient
	base   string
}

type runFeedPayload struct {
	Type      string          `json:"type"`
	Delta     string          `json:"delta,omitempty"`
	Message   string          `json:"message,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	Revision  uint64          `json:"revision,omitempty"`
	Status    string          `json:"status,omitempty"`
	Terminal  bool            `json:"terminal,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
}

// NewRunFeedStore creates the Redis Run Feed adapter.
func NewRunFeedStore(client goredis.UniversalClient, options RunFeedOptions) *RunFeedStore {
	prefix := strings.TrimSpace(options.KeyPrefix)
	if prefix == "" {
		prefix = defaultKeyPrefix
	}
	if !strings.HasSuffix(prefix, ":") {
		prefix += ":"
	}
	return &RunFeedStore{client: client, base: prefix + "{agent-runtime}:runfeed:"}
}

// Append atomically assigns a Run-local sequence and refreshes all retention keys.
func (store *RunFeedStore) Append(
	ctx context.Context,
	runID string,
	draft runfeed.Draft,
	createdAt time.Time,
	retention time.Duration,
) (runfeed.Event, error) {
	runID = strings.TrimSpace(runID)
	if store == nil || store.client == nil || runID == "" || strings.TrimSpace(draft.Type) == "" || retention <= 0 ||
		(len(draft.Data) > 0 && !json.Valid(draft.Data)) {
		return runfeed.Event{}, runfeed.ErrInvalidInput
	}
	payload, err := json.Marshal(runFeedPayload{
		Type: strings.TrimSpace(draft.Type), Delta: draft.Delta, Message: strings.TrimSpace(draft.Message),
		Data: append(json.RawMessage(nil), draft.Data...), Revision: draft.Revision,
		Status: strings.TrimSpace(draft.Status), Terminal: draft.Terminal, CreatedAt: createdAt.UTC(),
	})
	if err != nil {
		return runfeed.Event{}, err
	}
	retentionMilliseconds := max(int64(1), retention.Milliseconds())
	terminal := "0"
	if draft.Terminal {
		terminal = "1"
	}
	sequence, err := appendRunFeedScript.Run(
		ctx, store.client, store.keys(runID), string(payload), retentionMilliseconds, terminal,
	).Int64()
	if err != nil {
		return runfeed.Event{}, err
	}
	return eventFromRunFeedPayload(runID, sequence, payload)
}

// List returns retained events strictly after afterSeq in ascending sequence order.
func (store *RunFeedStore) List(ctx context.Context, runID string, afterSeq int64, limit int) ([]runfeed.Event, error) {
	runID = strings.TrimSpace(runID)
	if store == nil || store.client == nil || runID == "" || afterSeq < 0 {
		return nil, runfeed.ErrInvalidInput
	}
	if limit <= 0 {
		limit = 128
	}
	sequences, values, err := store.loadRunFeedPayloads(ctx, runID, afterSeq, limit)
	if err != nil {
		return nil, err
	}
	if len(sequences) == 0 {
		headSeq, headErr := store.runFeedHead(ctx, runID)
		if headErr != nil {
			return nil, headErr
		}
		if headSeq > afterSeq {
			return nil, &runfeed.CursorExpiredError{AfterSeq: afterSeq, HeadSeq: headSeq}
		}
		return nil, nil
	}
	firstSeq, err := strconv.ParseInt(sequences[0], 10, 64)
	if err != nil {
		return nil, errors.Join(runfeed.ErrInvalidInput, err)
	}
	if firstSeq > afterSeq+1 {
		headSeq, headErr := store.runFeedHead(ctx, runID)
		if headErr != nil {
			return nil, headErr
		}
		return nil, &runfeed.CursorExpiredError{AfterSeq: afterSeq, HeadSeq: headSeq}
	}
	return decodeRunFeedEvents(runID, sequences, values)
}

func (store *RunFeedStore) runFeedHead(ctx context.Context, runID string) (int64, error) {
	head, err := store.client.Get(ctx, store.keys(runID)[0]).Int64()
	if errors.Is(err, goredis.Nil) {
		return 0, nil
	}
	return head, err
}

func (store *RunFeedStore) loadRunFeedPayloads(
	ctx context.Context,
	runID string,
	afterSeq int64,
	limit int,
) ([]string, []interface{}, error) {
	keys := store.keys(runID)
	sequences, err := store.client.ZRangeByScore(ctx, keys[1], &goredis.ZRangeBy{
		Min: "(" + strconv.FormatInt(afterSeq, 10), Max: "+inf", Offset: 0, Count: int64(limit),
	}).Result()
	if err != nil || len(sequences) == 0 {
		return sequences, nil, err
	}
	values, err := store.client.HMGet(ctx, keys[2], sequences...).Result()
	return sequences, values, err
}

func decodeRunFeedEvents(runID string, sequences []string, values []interface{}) ([]runfeed.Event, error) {
	result := make([]runfeed.Event, 0, len(values))
	for index, value := range values {
		if value == nil {
			continue
		}
		sequence, parseErr := strconv.ParseInt(sequences[index], 10, 64)
		if parseErr != nil {
			return nil, errors.Join(runfeed.ErrInvalidInput, parseErr)
		}
		event, parseErr := eventFromRunFeedPayload(runID, sequence, []byte(fmt.Sprint(value)))
		if parseErr != nil {
			return nil, parseErr
		}
		result = append(result, event)
	}
	return result, nil
}

func eventFromRunFeedPayload(runID string, sequence int64, raw []byte) (runfeed.Event, error) {
	var payload runFeedPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return runfeed.Event{}, errors.Join(runfeed.ErrInvalidInput, err)
	}
	return runfeed.Event{
		Seq: sequence, RunID: runID, Type: payload.Type, Delta: payload.Delta, Message: payload.Message,
		Data: append(json.RawMessage(nil), payload.Data...), Revision: payload.Revision,
		Status: payload.Status, Terminal: payload.Terminal, CreatedAt: payload.CreatedAt.UTC(),
	}, nil
}

func (store *RunFeedStore) keys(runID string) []string {
	base := store.base + runID + ":"
	return []string{base + "sequence", base + "ordered", base + "payloads"}
}

var appendRunFeedScript = goredis.NewScript(`
local sequence = redis.call('INCR', KEYS[1])
redis.call('ZADD', KEYS[2], sequence, tostring(sequence))
redis.call('HSET', KEYS[3], tostring(sequence), ARGV[1])
local ttl = tonumber(ARGV[2])
if ARGV[3] == '1' then
  redis.call('PEXPIRE', KEYS[1], ttl)
else
  redis.call('PERSIST', KEYS[1])
end
redis.call('PEXPIRE', KEYS[2], ttl)
redis.call('PEXPIRE', KEYS[3], ttl)
return sequence
`)
