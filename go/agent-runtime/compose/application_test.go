package compose_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/compose"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

func TestApplicationValidatesCapabilitiesAndWorkerOrder(t *testing.T) {
	t.Parallel()
	log := make([]string, 0)
	provider := &testWorker{name: "provider", provides: []kernel.Capability{"test.provider"}, log: &log}
	consumer := &testWorker{name: "consumer", requires: []kernel.Capability{"test.provider"}, log: &log}
	application, err := compose.New(provider, consumer)
	if err != nil {
		t.Fatalf("compose application: %v", err)
	}
	if err = application.Start(context.Background()); err != nil {
		t.Fatalf("start application: %v", err)
	}
	if err = application.Close(context.Background()); err != nil {
		t.Fatalf("close application: %v", err)
	}
	expected := []string{"start:provider", "start:consumer", "close:consumer", "close:provider"}
	if len(log) != len(expected) {
		t.Fatalf("unexpected lifecycle log: %v", log)
	}
	for index := range expected {
		if log[index] != expected[index] {
			t.Fatalf("unexpected lifecycle log: %v", log)
		}
	}
}

func TestApplicationStartFailureRollsBackAndCanRetry(t *testing.T) {
	t.Parallel()
	log := make([]string, 0)
	startFailure := errors.New("start failure")
	one := &testWorker{name: "one", log: &log}
	two := &testWorker{name: "two", log: &log}
	three := &testWorker{name: "three", log: &log, startErr: startFailure, startFailures: 1}
	application, err := compose.New(one, two, three)
	if err != nil {
		t.Fatalf("compose application: %v", err)
	}
	if err = application.Start(t.Context()); !errors.Is(err, startFailure) {
		t.Fatalf("expected start failure, got %v", err)
	}
	assertLifecycleLog(t, log, []string{
		"start:one", "start:two", "start:three", "close:two", "close:one",
	})
	if err = application.Start(t.Context()); err != nil {
		t.Fatalf("retry start application: %v", err)
	}
	if err = application.Close(t.Context()); err != nil {
		t.Fatalf("close retried application: %v", err)
	}
	assertLifecycleLog(t, log, []string{
		"start:one", "start:two", "start:three", "close:two", "close:one",
		"start:one", "start:two", "start:three", "close:three", "close:two", "close:one",
	})
}

func TestApplicationLifecycleCallbacksMayReenterWithoutDeadlock(t *testing.T) {
	t.Parallel()
	var application *compose.Application
	var reentrantCloseErr error
	var reentrantStartErr error
	worker := &callbackWorker{name: "reentrant"}
	worker.start = func(ctx context.Context) error {
		reentrantCloseErr = application.Close(ctx)
		return nil
	}
	worker.close = func(ctx context.Context) error {
		reentrantStartErr = application.Start(ctx)
		return nil
	}
	var err error
	application, err = compose.New(worker)
	if err != nil {
		t.Fatalf("compose application: %v", err)
	}
	if err = application.Start(t.Context()); err != nil {
		t.Fatalf("start application: %v", err)
	}
	if !errors.Is(reentrantCloseErr, compose.ErrLifecycleTransition) {
		t.Fatalf("reentrant close = %v", reentrantCloseErr)
	}
	if err = application.Close(t.Context()); err != nil {
		t.Fatalf("close application: %v", err)
	}
	if !errors.Is(reentrantStartErr, compose.ErrClosed) {
		t.Fatalf("reentrant start = %v", reentrantStartErr)
	}
}

func TestApplicationConcurrentStartAndCloseFailFastDuringSlowStart(t *testing.T) {
	t.Parallel()
	startEntered := make(chan struct{})
	startRelease := make(chan struct{})
	worker := &blockingWorker{name: "slow-start", startEntered: startEntered, startRelease: startRelease}
	application, err := compose.New(worker)
	if err != nil {
		t.Fatalf("compose application: %v", err)
	}
	startDone := make(chan error, 1)
	go func() { startDone <- application.Start(t.Context()) }()
	waitSignal(t, startEntered)
	if err = application.Start(t.Context()); !errors.Is(err, compose.ErrLifecycleTransition) {
		t.Fatalf("concurrent start = %v", err)
	}
	if err = application.Close(t.Context()); !errors.Is(err, compose.ErrLifecycleTransition) {
		t.Fatalf("close during start = %v", err)
	}
	close(startRelease)
	if err = waitLifecycleResult(t, startDone); err != nil {
		t.Fatalf("slow start = %v", err)
	}
	if err = application.Close(t.Context()); err != nil {
		t.Fatalf("close after start = %v", err)
	}
}

func TestApplicationConcurrentCloseDoesNotSerializeOnPluginCallback(t *testing.T) {
	t.Parallel()
	closeEntered := make(chan struct{})
	closeRelease := make(chan struct{})
	worker := &blockingWorker{name: "slow-close", closeEntered: closeEntered, closeRelease: closeRelease}
	application, err := compose.New(worker)
	if err != nil {
		t.Fatalf("compose application: %v", err)
	}
	if err = application.Start(t.Context()); err != nil {
		t.Fatalf("start application: %v", err)
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- application.Close(t.Context()) }()
	waitSignal(t, closeEntered)
	if err = application.Close(t.Context()); !errors.Is(err, compose.ErrLifecycleTransition) {
		t.Fatalf("concurrent close = %v", err)
	}
	if err = application.Start(t.Context()); !errors.Is(err, compose.ErrClosed) {
		t.Fatalf("start during close = %v", err)
	}
	close(closeRelease)
	if err = waitLifecycleResult(t, closeDone); err != nil {
		t.Fatalf("slow close = %v", err)
	}
	if err = application.Close(t.Context()); err != nil {
		t.Fatalf("idempotent close = %v", err)
	}
}

func TestApplicationCloseFailureStillClosesAllWorkers(t *testing.T) {
	t.Parallel()
	log := make([]string, 0)
	closeFailure := errors.New("close failure")
	one := &testWorker{name: "one", log: &log}
	two := &testWorker{name: "two", log: &log, closeErr: closeFailure}
	application, err := compose.New(one, two)
	if err != nil {
		t.Fatalf("compose application: %v", err)
	}
	if err = application.Start(t.Context()); err != nil {
		t.Fatalf("start application: %v", err)
	}
	if err = application.Close(t.Context()); !errors.Is(err, closeFailure) {
		t.Fatalf("close application = %v", err)
	}
	assertLifecycleLog(t, log, []string{"start:one", "start:two", "close:two", "close:one"})
	if err = application.Close(t.Context()); err != nil {
		t.Fatalf("idempotent close after failure = %v", err)
	}
	if err = application.Start(t.Context()); !errors.Is(err, compose.ErrClosed) {
		t.Fatalf("start after failed close = %v", err)
	}
}

func TestApplicationRejectsMissingCapability(t *testing.T) {
	t.Parallel()
	_, err := compose.New(&testWorker{name: "consumer", requires: []kernel.Capability{"missing"}})
	if !errors.Is(err, compose.ErrMissingCapability) {
		t.Fatalf("expected missing capability, got %v", err)
	}
}

type testWorker struct {
	name          string
	requires      []kernel.Capability
	provides      []kernel.Capability
	log           *[]string
	startErr      error
	startFailures int
	closeErr      error
}

func (worker *testWorker) Descriptor() kernel.FeatureDescriptor {
	return kernel.FeatureDescriptor{Name: worker.name, Requires: worker.requires, Provides: worker.provides}
}

func (worker *testWorker) Start(context.Context) error {
	if worker.log != nil {
		*worker.log = append(*worker.log, "start:"+worker.name)
	}
	if worker.startFailures > 0 {
		worker.startFailures--
		return worker.startErr
	}
	return nil
}

func (worker *testWorker) Close(context.Context) error {
	if worker.log != nil {
		*worker.log = append(*worker.log, "close:"+worker.name)
	}
	return worker.closeErr
}

type callbackWorker struct {
	name  string
	start func(context.Context) error
	close func(context.Context) error
}

func (worker *callbackWorker) Descriptor() kernel.FeatureDescriptor {
	return kernel.FeatureDescriptor{Name: worker.name}
}

func (worker *callbackWorker) Start(ctx context.Context) error { return worker.start(ctx) }
func (worker *callbackWorker) Close(ctx context.Context) error { return worker.close(ctx) }

type blockingWorker struct {
	name         string
	startEntered chan struct{}
	startRelease <-chan struct{}
	closeEntered chan struct{}
	closeRelease <-chan struct{}
}

func (worker *blockingWorker) Descriptor() kernel.FeatureDescriptor {
	return kernel.FeatureDescriptor{Name: worker.name}
}

func (worker *blockingWorker) Start(ctx context.Context) error {
	if worker.startEntered != nil {
		close(worker.startEntered)
	}
	if worker.startRelease == nil {
		return nil
	}
	select {
	case <-worker.startRelease:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (worker *blockingWorker) Close(ctx context.Context) error {
	if worker.closeEntered != nil {
		close(worker.closeEntered)
	}
	if worker.closeRelease == nil {
		return nil
	}
	select {
	case <-worker.closeRelease:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func assertLifecycleLog(t *testing.T, got []string, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("lifecycle log = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("lifecycle log = %v, want %v", got, want)
		}
	}
}

func waitSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal("lifecycle callback did not start")
	}
}

func waitLifecycleResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-time.After(time.Second):
		t.Fatal("lifecycle operation did not finish")
		return nil
	}
}
