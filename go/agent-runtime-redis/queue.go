package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	goredis "github.com/go-redis/redis/v8"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
	queuecore "github.com/orz-i/Gaoge/sdk/go/agent-runtime/queue"
)

// QueueOptions configures the Redis delivery adapter.
type QueueOptions struct {
	KeyPrefix string
	Clock     kernel.Clock
}

// DeliveryQueue implements Queue Job/Lease state using Redis Lua transactions.
type DeliveryQueue struct {
	client goredis.UniversalClient
	base   string
	clock  kernel.Clock
}

// NewQueue creates the Redis Queue adapter.
func NewQueue(client goredis.UniversalClient, options QueueOptions) *DeliveryQueue {
	prefix := strings.TrimSpace(options.KeyPrefix)
	if prefix == "" {
		prefix = defaultKeyPrefix
	}
	if !strings.HasSuffix(prefix, ":") {
		prefix += ":"
	}
	clock := options.Clock
	if clock == nil {
		clock = redisSystemClock{}
	}
	return &DeliveryQueue{client: client, base: prefix + "{agent-runtime}:queue:", clock: clock}
}

// Descriptor declares the Queue delivery capability.
func (queue *DeliveryQueue) Descriptor() kernel.FeatureDescriptor {
	return kernel.FeatureDescriptor{Name: "redis-queue", Provides: []kernel.Capability{queuecore.CapabilityQueue}}
}

// Enqueue atomically creates or reuses one canonical Job.
func (queue *DeliveryQueue) Enqueue(
	ctx context.Context,
	request queuecore.EnqueueRequest,
) (queuecore.EnqueueResult, error) {
	if queue == nil || queue.client == nil {
		return queuecore.EnqueueResult{}, queuecore.ErrInvalidInput
	}
	job, err := queuecore.PrepareEnqueue(request, queue.now())
	if err != nil {
		return queuecore.EnqueueResult{}, err
	}
	code, err := enqueueQueueScript.Run(ctx, queue.client, []string{
		queue.jobKey(job.ID), queue.indexKey(job.ID), queue.readyKey(job.Queue), queue.allKey(job.Queue),
	}, enqueueArgs(job)...).Int64()
	if err != nil {
		return queuecore.EnqueueResult{}, err
	}
	switch code {
	case -1:
		return queuecore.EnqueueResult{}, queuecore.ErrConflict
	case 0:
		return queuecore.EnqueueResult{Job: job}, nil
	case 1:
		existing, loadErr := queue.Get(ctx, job.ID)
		return queuecore.EnqueueResult{Job: existing, Reused: true}, loadErr
	default:
		return queuecore.EnqueueResult{}, queuecore.ErrInvalidInput
	}
}

// Claim atomically leases eligible Jobs in priority order.
func (queue *DeliveryQueue) Claim(
	ctx context.Context,
	request queuecore.ClaimRequest,
) ([]queuecore.Delivery, error) {
	request, err := normalizeRedisClaimRequest(request)
	if err != nil || queue == nil || queue.client == nil {
		return nil, queuecore.ErrInvalidInput
	}
	result, err := claimQueueScript.Run(ctx, queue.client, []string{
		queue.readyKey(request.Queue), queue.leasedKey(request.Queue),
	}, queue.now().UnixMilli(), request.Limit, request.WorkerID, queue.jobPrefix()).Result()
	if err != nil {
		return nil, err
	}
	return queue.claimDeliveries(ctx, result)
}

func normalizeRedisClaimRequest(request queuecore.ClaimRequest) (queuecore.ClaimRequest, error) {
	request.Queue = strings.TrimSpace(request.Queue)
	request.WorkerID = strings.TrimSpace(request.WorkerID)
	if request.Limit <= 0 {
		request.Limit = 1
	}
	if request.Queue == "" || request.WorkerID == "" || request.Limit > 100 {
		return queuecore.ClaimRequest{}, queuecore.ErrInvalidInput
	}
	return request, nil
}

func (queue *DeliveryQueue) claimDeliveries(
	ctx context.Context,
	result interface{},
) ([]queuecore.Delivery, error) {
	values, ok := result.([]interface{})
	if !ok || len(values)%2 != 0 {
		return nil, queuecore.ErrInvalidInput
	}
	deliveries := make([]queuecore.Delivery, 0, len(values)/2)
	for index := 0; index < len(values); index += 2 {
		jobID := fmt.Sprint(values[index])
		leaseID := fmt.Sprint(values[index+1])
		job, loadErr := queue.Get(ctx, jobID)
		if loadErr != nil {
			return nil, loadErr
		}
		if job.Lease == nil || job.Lease.ID != leaseID {
			return nil, queuecore.ErrLeaseLost
		}
		deliveries = append(deliveries, queuecore.Delivery{Job: job, Lease: *job.Lease})
	}
	return deliveries, nil
}

// Renew atomically extends the current Lease.
func (queue *DeliveryQueue) Renew(
	ctx context.Context,
	request queuecore.LeaseRequest,
) (queuecore.Delivery, error) {
	jobKey, job, err := queue.resolveJob(ctx, request.JobID)
	if err != nil {
		return queuecore.Delivery{}, err
	}
	code, err := renewQueueScript.Run(ctx, queue.client, []string{
		jobKey, queue.readyKey(job.Queue), queue.leasedKey(job.Queue),
	}, queue.now().UnixMilli(), strings.TrimSpace(request.LeaseID), strings.TrimSpace(request.WorkerID), job.ID).Int64()
	if err != nil {
		return queuecore.Delivery{}, err
	}
	if err = queue.leaseCodeError(code); err != nil {
		return queuecore.Delivery{}, err
	}
	updated, err := queue.Get(ctx, job.ID)
	if err != nil || updated.Lease == nil {
		return queuecore.Delivery{}, errors.Join(queuecore.ErrLeaseLost, err)
	}
	return queuecore.Delivery{Job: updated, Lease: *updated.Lease}, nil
}

// Ack atomically completes the current Lease generation.
func (queue *DeliveryQueue) Ack(ctx context.Context, request queuecore.LeaseRequest) (queuecore.Job, error) {
	jobKey, job, err := queue.resolveJob(ctx, request.JobID)
	if err != nil {
		return queuecore.Job{}, err
	}
	code, err := ackQueueScript.Run(ctx, queue.client, []string{
		jobKey, queue.readyKey(job.Queue), queue.leasedKey(job.Queue),
	}, queue.now().UnixMilli(), strings.TrimSpace(request.LeaseID), strings.TrimSpace(request.WorkerID), job.ID).Int64()
	if err != nil {
		return queuecore.Job{}, err
	}
	if err = queue.leaseCodeError(code); err != nil {
		return queuecore.Job{}, err
	}
	return queue.Get(ctx, job.ID)
}

// Nack atomically schedules retry or Dead Letter for the current Lease.
func (queue *DeliveryQueue) Nack(ctx context.Context, request queuecore.NackRequest) (queuecore.Job, error) {
	jobKey, job, err := queue.resolveJob(ctx, request.JobID)
	if err != nil {
		return queuecore.Job{}, err
	}
	code, err := nackQueueScript.Run(ctx, queue.client, []string{
		jobKey, queue.readyKey(job.Queue), queue.leasedKey(job.Queue),
	}, queue.now().UnixMilli(), strings.TrimSpace(request.LeaseID), strings.TrimSpace(request.WorkerID),
		job.ID, strings.TrimSpace(request.ErrorCode), truncateQueueText(request.Error, 1_024)).Int64()
	if err != nil {
		return queuecore.Job{}, err
	}
	if err = queue.leaseCodeError(code); err != nil {
		return queuecore.Job{}, err
	}
	return queue.Get(ctx, job.ID)
}

// Reap atomically expires all due Leases for one Queue.
func (queue *DeliveryQueue) Reap(ctx context.Context, queueName string) (int, error) {
	queueName = strings.TrimSpace(queueName)
	if queue == nil || queue.client == nil || queueName == "" {
		return 0, queuecore.ErrInvalidInput
	}
	count, err := reapQueueScript.Run(ctx, queue.client, []string{
		queue.readyKey(queueName), queue.leasedKey(queueName),
	}, queue.now().UnixMilli(), queue.jobPrefix()).Int()
	return count, err
}

// Get returns one immutable Job snapshot.
func (queue *DeliveryQueue) Get(ctx context.Context, jobID string) (queuecore.Job, error) {
	_, job, err := queue.resolveJob(ctx, jobID)
	return job, err
}

// List returns Queue jobs in deterministic creation order.
func (queue *DeliveryQueue) List(
	ctx context.Context,
	queueName string,
	status queuecore.Status,
) ([]queuecore.Job, error) {
	queueName = strings.TrimSpace(queueName)
	if queue == nil || queue.client == nil || queueName == "" {
		return nil, queuecore.ErrInvalidInput
	}
	ids, err := queue.client.SMembers(ctx, queue.allKey(queueName)).Result()
	if err != nil {
		return nil, err
	}
	return queue.listJobs(ctx, ids, status)
}

func (queue *DeliveryQueue) listJobs(
	ctx context.Context,
	ids []string,
	status queuecore.Status,
) ([]queuecore.Job, error) {
	result := make([]queuecore.Job, 0, len(ids))
	for _, jobID := range ids {
		job, loadErr := queue.Get(ctx, jobID)
		if errors.Is(loadErr, queuecore.ErrNotFound) {
			continue
		}
		if loadErr != nil {
			return nil, loadErr
		}
		if status == "" || job.Status == status {
			result = append(result, job)
		}
	}
	sort.Slice(result, func(left int, right int) bool {
		if result[left].CreatedAt.Equal(result[right].CreatedAt) {
			return result[left].ID < result[right].ID
		}
		return result[left].CreatedAt.Before(result[right].CreatedAt)
	})
	return result, nil
}

// RequeueDeadLetter atomically resets one terminal Job for operator replay.
func (queue *DeliveryQueue) RequeueDeadLetter(ctx context.Context, jobID string) (queuecore.Job, error) {
	jobKey, job, err := queue.resolveJob(ctx, jobID)
	if err != nil {
		return queuecore.Job{}, err
	}
	code, err := requeueQueueScript.Run(ctx, queue.client, []string{
		jobKey, queue.readyKey(job.Queue), queue.leasedKey(job.Queue),
	}, queue.now().UnixMilli(), job.ID).Int64()
	if err != nil {
		return queuecore.Job{}, err
	}
	if code == -3 {
		return queuecore.Job{}, queuecore.ErrJobTerminal
	}
	if code != 1 {
		return queuecore.Job{}, queuecore.ErrNotFound
	}
	return queue.Get(ctx, job.ID)
}

func (queue *DeliveryQueue) resolveJob(
	ctx context.Context,
	jobID string,
) (string, queuecore.Job, error) {
	if queue == nil || queue.client == nil {
		return "", queuecore.Job{}, queuecore.ErrInvalidInput
	}
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return "", queuecore.Job{}, queuecore.ErrNotFound
	}
	jobKey, err := queue.client.Get(ctx, queue.indexKey(jobID)).Result()
	if errors.Is(err, goredis.Nil) {
		return "", queuecore.Job{}, queuecore.ErrNotFound
	}
	if err != nil {
		return "", queuecore.Job{}, err
	}
	values, err := queue.client.HGetAll(ctx, jobKey).Result()
	if err != nil {
		return "", queuecore.Job{}, err
	}
	if len(values) == 0 {
		return "", queuecore.Job{}, queuecore.ErrNotFound
	}
	job, err := redisJob(values)
	return jobKey, job, err
}

func (queue *DeliveryQueue) leaseCodeError(code int64) error {
	switch code {
	case 1:
		return nil
	case -1:
		return queuecore.ErrNotFound
	case -2:
		return queuecore.ErrLeaseExpired
	case -3:
		return queuecore.ErrJobTerminal
	default:
		return queuecore.ErrLeaseLost
	}
}

func enqueueArgs(job queuecore.Job) []interface{} {
	values := map[string]string{
		"id": job.ID, "queue": job.Queue, "client_job_id": job.ClientJobID,
		"fingerprint": job.Fingerprint, "kind": job.Kind, "payload": string(job.Payload),
		"priority": strconv.Itoa(job.Priority), "max_attempts": strconv.Itoa(job.Policy.MaxAttempts),
		"visibility_ms":      strconv.FormatInt(job.Policy.VisibilityTimeout.Milliseconds(), 10),
		"initial_backoff_ms": strconv.FormatInt(job.Policy.InitialBackoff.Milliseconds(), 10),
		"max_backoff_ms":     strconv.FormatInt(job.Policy.MaxBackoff.Milliseconds(), 10),
		"backoff_multiplier": strconv.Itoa(job.Policy.BackoffMultiplier),
		"status":             string(job.Status), "attempt": "0", "generation": "0",
		"available_ms": strconv.FormatInt(job.AvailableAt.UnixMilli(), 10),
		"created_ms":   strconv.FormatInt(job.CreatedAt.UnixMilli(), 10),
		"updated_ms":   strconv.FormatInt(job.UpdatedAt.UnixMilli(), 10),
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	args := make([]interface{}, 0, len(keys)*2+1)
	args = append(args, job.Fingerprint)
	for _, key := range keys {
		args = append(args, key, values[key])
	}
	return args
}

func redisJob(values map[string]string) (queuecore.Job, error) {
	payload := json.RawMessage(values["payload"])
	if !json.Valid(payload) {
		return queuecore.Job{}, queuecore.ErrInvalidInput
	}
	priority, err := parseInt(values, "priority")
	if err != nil {
		return queuecore.Job{}, err
	}
	attempt, err := parseInt(values, "attempt")
	if err != nil {
		return queuecore.Job{}, err
	}
	generation, err := parseUint(values, "generation")
	if err != nil {
		return queuecore.Job{}, err
	}
	policy, err := redisPolicy(values)
	if err != nil {
		return queuecore.Job{}, err
	}
	job := queuecore.Job{
		ID: values["id"], Queue: values["queue"], ClientJobID: values["client_job_id"],
		Fingerprint: values["fingerprint"], Kind: values["kind"], Payload: append(json.RawMessage(nil), payload...),
		Priority: priority, Policy: policy, Status: queuecore.Status(values["status"]),
		Attempt: attempt, Generation: generation,
		AvailableAt: millisTime(values["available_ms"]), CreatedAt: millisTime(values["created_ms"]),
		UpdatedAt: millisTime(values["updated_ms"]), LastErrorCode: values["last_error_code"],
		LastError: values["last_error"],
	}
	if job.Status == queuecore.StatusLeased {
		leaseGeneration, parseErr := parseUint(values, "generation")
		if parseErr != nil {
			return queuecore.Job{}, parseErr
		}
		job.Lease = &queuecore.Lease{
			ID: values["lease_id"], WorkerID: values["worker_id"], Generation: leaseGeneration,
			Attempt: job.Attempt, ClaimedAt: millisTime(values["claimed_ms"]), ExpiresAt: millisTime(values["expires_ms"]),
		}
	}
	job.CompletedAt = optionalMillisTime(values["completed_ms"])
	job.DeadLetterAt = optionalMillisTime(values["dead_letter_ms"])
	return job, nil
}

func redisPolicy(values map[string]string) (queuecore.Policy, error) {
	maxAttempts, err := parseInt(values, "max_attempts")
	if err != nil {
		return queuecore.Policy{}, err
	}
	multiplier, err := parseInt(values, "backoff_multiplier")
	if err != nil {
		return queuecore.Policy{}, err
	}
	visibility, err := parseInt64(values, "visibility_ms")
	if err != nil {
		return queuecore.Policy{}, err
	}
	initial, err := parseInt64(values, "initial_backoff_ms")
	if err != nil {
		return queuecore.Policy{}, err
	}
	maximum, err := parseInt64(values, "max_backoff_ms")
	if err != nil {
		return queuecore.Policy{}, err
	}
	return queuecore.Policy{
		MaxAttempts: maxAttempts, VisibilityTimeout: time.Duration(visibility) * time.Millisecond,
		InitialBackoff: time.Duration(initial) * time.Millisecond,
		MaxBackoff:     time.Duration(maximum) * time.Millisecond, BackoffMultiplier: multiplier,
	}, nil
}

func parseInt(values map[string]string, key string) (int, error) {
	value, err := strconv.Atoi(values[key])
	if err != nil {
		return 0, queuecore.ErrInvalidInput
	}
	return value, nil
}

func parseInt64(values map[string]string, key string) (int64, error) {
	value, err := strconv.ParseInt(values[key], 10, 64)
	if err != nil {
		return 0, queuecore.ErrInvalidInput
	}
	return value, nil
}

func parseUint(values map[string]string, key string) (uint64, error) {
	value, err := strconv.ParseUint(values[key], 10, 64)
	if err != nil {
		return 0, queuecore.ErrInvalidInput
	}
	return value, nil
}

func millisTime(value string) time.Time {
	millis, _ := strconv.ParseInt(value, 10, 64)
	return time.UnixMilli(millis).UTC()
}

func optionalMillisTime(value string) *time.Time {
	if value == "" || value == "0" {
		return nil
	}
	parsed := millisTime(value)
	return &parsed
}

func truncateQueueText(value string, limit int) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if limit <= 0 {
		return ""
	}
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit-1]) + "…"
}

func (queue *DeliveryQueue) now() time.Time { return queue.clock.Now().UTC() }

func (queue *DeliveryQueue) jobPrefix() string            { return queue.base + "job:" }
func (queue *DeliveryQueue) jobKey(jobID string) string   { return queue.jobPrefix() + jobID }
func (queue *DeliveryQueue) indexKey(jobID string) string { return queue.base + "index:" + jobID }
func (queue *DeliveryQueue) readyKey(name string) string  { return queue.base + "ready:" + name }
func (queue *DeliveryQueue) leasedKey(name string) string { return queue.base + "leased:" + name }
func (queue *DeliveryQueue) allKey(name string) string    { return queue.base + "all:" + name }

type redisSystemClock struct{}

func (redisSystemClock) Now() time.Time { return time.Now() }

var enqueueQueueScript = goredis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 1 then
  if redis.call('HGET', KEYS[1], 'fingerprint') == ARGV[1] then return 1 end
  return -1
end
for i = 2, #ARGV, 2 do redis.call('HSET', KEYS[1], ARGV[i], ARGV[i + 1]) end
redis.call('SET', KEYS[2], KEYS[1])
redis.call('ZADD', KEYS[3], redis.call('HGET', KEYS[1], 'available_ms'), redis.call('HGET', KEYS[1], 'id'))
redis.call('SADD', KEYS[4], redis.call('HGET', KEYS[1], 'id'))
return 0
`)

var claimQueueScript = goredis.NewScript(`
local due = redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
local candidates = {}
for _, id in ipairs(due) do
  local job = ARGV[4] .. id
  if redis.call('HGET', job, 'status') == 'queued' then
    table.insert(candidates, {
      id = id,
      priority = tonumber(redis.call('HGET', job, 'priority')) or 0,
      available = tonumber(redis.call('HGET', job, 'available_ms')) or 0,
      created = tonumber(redis.call('HGET', job, 'created_ms')) or 0
    })
  end
end
table.sort(candidates, function(a, b)
  if a.priority ~= b.priority then return a.priority > b.priority end
  if a.available ~= b.available then return a.available < b.available end
  if a.created ~= b.created then return a.created < b.created end
  return a.id < b.id
end)
local output = {}
local limit = tonumber(ARGV[2])
for index = 1, math.min(limit, #candidates) do
  local id = candidates[index].id
  local job = ARGV[4] .. id
  local attempt = redis.call('HINCRBY', job, 'attempt', 1)
  local generation = redis.call('HINCRBY', job, 'generation', 1)
  local lease = 'lease:' .. id .. ':' .. generation
  local expires = tonumber(ARGV[1]) + tonumber(redis.call('HGET', job, 'visibility_ms'))
  redis.call('HSET', job,
    'status', 'leased', 'lease_id', lease, 'worker_id', ARGV[3],
    'claimed_ms', ARGV[1], 'expires_ms', expires, 'updated_ms', ARGV[1])
  redis.call('ZREM', KEYS[1], id)
  redis.call('ZADD', KEYS[2], expires, id)
  table.insert(output, id)
  table.insert(output, lease)
end
return output
`)

const releaseLua = `
local function release(job, ready, leased, id, now)
  local attempt = tonumber(redis.call('HGET', job, 'attempt')) or 0
  local max_attempts = tonumber(redis.call('HGET', job, 'max_attempts')) or 1
  redis.call('ZREM', leased, id)
  redis.call('HDEL', job, 'lease_id', 'worker_id', 'claimed_ms', 'expires_ms')
  if attempt >= max_attempts then
    redis.call('HSET', job, 'status', 'dead_letter', 'dead_letter_ms', now, 'updated_ms', now)
    return 2
  end
  local delay = tonumber(redis.call('HGET', job, 'initial_backoff_ms')) or 0
  local maximum = tonumber(redis.call('HGET', job, 'max_backoff_ms')) or delay
  local multiplier = tonumber(redis.call('HGET', job, 'backoff_multiplier')) or 2
  for index = 2, attempt do
    if delay > maximum / multiplier then delay = maximum break end
    delay = delay * multiplier
  end
  if delay > maximum then delay = maximum end
  local available = now + delay
  redis.call('HSET', job, 'status', 'queued', 'available_ms', available, 'updated_ms', now)
  redis.call('ZADD', ready, available, id)
  return 1
end
`

var renewQueueScript = goredis.NewScript(releaseLua + `
if redis.call('EXISTS', KEYS[1]) == 0 then return -1 end
local status = redis.call('HGET', KEYS[1], 'status')
if status == 'completed' or status == 'dead_letter' then return -3 end
if status ~= 'leased' or redis.call('HGET', KEYS[1], 'lease_id') ~= ARGV[2] or redis.call('HGET', KEYS[1], 'worker_id') ~= ARGV[3] then return 0 end
if tonumber(redis.call('HGET', KEYS[1], 'expires_ms')) <= tonumber(ARGV[1]) then
  release(KEYS[1], KEYS[2], KEYS[3], ARGV[4], tonumber(ARGV[1]))
  return -2
end
local expires = tonumber(ARGV[1]) + tonumber(redis.call('HGET', KEYS[1], 'visibility_ms'))
redis.call('HSET', KEYS[1], 'expires_ms', expires, 'updated_ms', ARGV[1])
redis.call('ZADD', KEYS[3], expires, ARGV[4])
return 1
`)

var ackQueueScript = goredis.NewScript(releaseLua + `
if redis.call('EXISTS', KEYS[1]) == 0 then return -1 end
local status = redis.call('HGET', KEYS[1], 'status')
if status == 'completed' or status == 'dead_letter' then return -3 end
if status ~= 'leased' or redis.call('HGET', KEYS[1], 'lease_id') ~= ARGV[2] or redis.call('HGET', KEYS[1], 'worker_id') ~= ARGV[3] then return 0 end
if tonumber(redis.call('HGET', KEYS[1], 'expires_ms')) <= tonumber(ARGV[1]) then
  release(KEYS[1], KEYS[2], KEYS[3], ARGV[4], tonumber(ARGV[1]))
  return -2
end
redis.call('ZREM', KEYS[3], ARGV[4])
redis.call('HDEL', KEYS[1], 'lease_id', 'worker_id', 'claimed_ms', 'expires_ms')
redis.call('HSET', KEYS[1], 'status', 'completed', 'completed_ms', ARGV[1], 'updated_ms', ARGV[1])
return 1
`)

var nackQueueScript = goredis.NewScript(releaseLua + `
if redis.call('EXISTS', KEYS[1]) == 0 then return -1 end
local status = redis.call('HGET', KEYS[1], 'status')
if status == 'completed' or status == 'dead_letter' then return -3 end
if status ~= 'leased' or redis.call('HGET', KEYS[1], 'lease_id') ~= ARGV[2] or redis.call('HGET', KEYS[1], 'worker_id') ~= ARGV[3] then return 0 end
if tonumber(redis.call('HGET', KEYS[1], 'expires_ms')) <= tonumber(ARGV[1]) then
  release(KEYS[1], KEYS[2], KEYS[3], ARGV[4], tonumber(ARGV[1]))
  return -2
end
redis.call('HSET', KEYS[1], 'last_error_code', ARGV[5], 'last_error', ARGV[6])
release(KEYS[1], KEYS[2], KEYS[3], ARGV[4], tonumber(ARGV[1]))
return 1
`)

var reapQueueScript = goredis.NewScript(releaseLua + `
local expired = redis.call('ZRANGEBYSCORE', KEYS[2], '-inf', ARGV[1])
local count = 0
for _, id in ipairs(expired) do
  local job = ARGV[2] .. id
  if redis.call('HGET', job, 'status') == 'leased' and tonumber(redis.call('HGET', job, 'expires_ms')) <= tonumber(ARGV[1]) then
    release(job, KEYS[1], KEYS[2], id, tonumber(ARGV[1]))
    count = count + 1
  else
    redis.call('ZREM', KEYS[2], id)
  end
end
return count
`)

var requeueQueueScript = goredis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return -1 end
if redis.call('HGET', KEYS[1], 'status') ~= 'dead_letter' then return -3 end
redis.call('ZREM', KEYS[3], ARGV[2])
redis.call('HDEL', KEYS[1], 'lease_id', 'worker_id', 'claimed_ms', 'expires_ms', 'completed_ms', 'dead_letter_ms')
redis.call('HSET', KEYS[1], 'status', 'queued', 'attempt', 0, 'available_ms', ARGV[1],
  'last_error_code', '', 'last_error', '', 'updated_ms', ARGV[1])
redis.call('ZADD', KEYS[2], ARGV[1], ARGV[2])
return 1
`)
