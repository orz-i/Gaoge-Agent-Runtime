package transport

import (
	"context"
	"time"
)

const (
	mediaPollDefaultTimeout  = 10 * time.Minute
	mediaPollDefaultInterval = 2 * time.Second
	mediaPollMinimumInterval = 100 * time.Millisecond
	mediaPollMaximumInterval = 30 * time.Second
	mediaPollIntervalOption  = "poll_interval_ms"
	mediaPollTimeoutOption   = "poll_timeout_ms"
)

func resolveMediaPollTimeout(route RouteConfig, options map[string]interface{}) time.Duration {
	timeout := mediaPollDefaultTimeout
	if route.ReadTimeoutMS > 0 {
		timeout = time.Duration(route.ReadTimeoutMS) * time.Millisecond
	}
	if value, ok := modelParamIntValue(options, mediaPollTimeoutOption); ok && value > 0 {
		timeout = time.Duration(value) * time.Millisecond
	}
	return timeout
}

func resolveMediaPollInterval(options map[string]interface{}) time.Duration {
	interval := mediaPollDefaultInterval
	if value, ok := modelParamIntValue(options, mediaPollIntervalOption); ok && value > 0 {
		interval = time.Duration(value) * time.Millisecond
	}
	if interval < mediaPollMinimumInterval {
		return mediaPollMinimumInterval
	}
	if interval > mediaPollMaximumInterval {
		return mediaPollMaximumInterval
	}
	return interval
}

func waitMediaPoll(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
