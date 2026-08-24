package compose_test

import (
	"context"
	"errors"
	"testing"

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

func TestApplicationRejectsMissingCapability(t *testing.T) {
	t.Parallel()
	_, err := compose.New(&testWorker{name: "consumer", requires: []kernel.Capability{"missing"}})
	if !errors.Is(err, compose.ErrMissingCapability) {
		t.Fatalf("expected missing capability, got %v", err)
	}
}

type testWorker struct {
	name     string
	requires []kernel.Capability
	provides []kernel.Capability
	log      *[]string
}

func (worker *testWorker) Descriptor() kernel.FeatureDescriptor {
	return kernel.FeatureDescriptor{Name: worker.name, Requires: worker.requires, Provides: worker.provides}
}

func (worker *testWorker) Start(context.Context) error {
	if worker.log != nil {
		*worker.log = append(*worker.log, "start:"+worker.name)
	}
	return nil
}

func (worker *testWorker) Close(context.Context) error {
	if worker.log != nil {
		*worker.log = append(*worker.log, "close:"+worker.name)
	}
	return nil
}
