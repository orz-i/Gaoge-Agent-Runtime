# Agent Runtime Redis Stream Adapter

`github.com/orz-i/Gaoge/sdk/go/agent-runtime-redis` implements ephemeral run
notifications, replay, cancellation signals and leases. Durable Run, Output,
Checkpoint and Queue facts remain in the Core Store.

```go
stream := redisruntime.New(client, redisruntime.Options{KeyPrefix: "runtime:"})
```

Select this adapter explicitly in the host composition root. Redis does not
perform an implicit fallback.
