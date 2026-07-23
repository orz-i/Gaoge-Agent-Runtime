package agentruntime

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestNewRejectsMissingDependenciesWithoutStartingWork(t *testing.T) {
	_, err := New(nil, Dependencies{})
	if !errors.Is(err, ErrMissingDependency) {
		t.Fatalf("New() error = %v, want %v", err, ErrMissingDependency)
	}
	_, err = New(StaticConfigProvider(Config{}), Dependencies{})
	if !errors.Is(err, ErrMissingDependency) {
		t.Fatalf("New() store error = %v, want %v", err, ErrMissingDependency)
	}
}

func TestEngineLifecycleStartsOnceAndClosesIdempotently(t *testing.T) {
	engine := &Engine{runQueueWake: make(chan struct{}, 1)}
	if err := engine.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := engine.Start(t.Context()); !errors.Is(err, ErrEngineAlreadyStarted) {
		t.Fatalf("second Start() error = %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := engine.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(ctx); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if err := engine.Start(t.Context()); !errors.Is(err, ErrEngineClosed) {
		t.Fatalf("Start() after Close() error = %v", err)
	}
}
