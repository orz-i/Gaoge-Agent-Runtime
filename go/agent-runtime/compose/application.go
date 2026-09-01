package compose

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

const lifecycleRollbackTimeout = 5 * time.Second

var (
	ErrInvalidFeature      = errors.New("invalid runtime feature")
	ErrDuplicateCapability = errors.New("duplicate runtime capability")
	ErrMissingCapability   = errors.New("missing runtime capability")
	ErrAlreadyStarted      = errors.New("runtime application already started")
	ErrLifecycleTransition = errors.New("runtime application lifecycle transition in progress")
	ErrClosed              = errors.New("runtime application is closed")
)

type lifecycleState uint8

const (
	lifecycleNew lifecycleState = iota
	lifecycleStarting
	lifecycleStarted
	lifecycleClosing
	lifecycleClosed
)

// Application validates capabilities and owns only explicitly composed workers.
type Application struct {
	features []kernel.Feature
	workers  []kernel.WorkerFeature
	mu       sync.Mutex
	state    lifecycleState
}

// New validates a fully constructed feature graph without locating services dynamically.
func New(features ...kernel.Feature) (*Application, error) {
	if len(features) == 0 {
		return nil, ErrInvalidFeature
	}
	provided, workers, err := inspectFeatures(features)
	if err != nil {
		return nil, err
	}
	if err = validateRequirements(features, provided); err != nil {
		return nil, err
	}
	return &Application{features: append([]kernel.Feature(nil), features...), workers: workers}, nil
}

// Start starts only worker features supplied by the host.
func (application *Application) Start(ctx context.Context) error {
	application.mu.Lock()
	switch application.state {
	case lifecycleClosed, lifecycleClosing:
		application.mu.Unlock()
		return ErrClosed
	case lifecycleStarting:
		application.mu.Unlock()
		return ErrLifecycleTransition
	case lifecycleStarted:
		application.mu.Unlock()
		return ErrAlreadyStarted
	case lifecycleNew:
		application.state = lifecycleStarting
	}
	workers := append([]kernel.WorkerFeature(nil), application.workers...)
	application.mu.Unlock()

	started := make([]kernel.WorkerFeature, 0, len(workers))
	for _, worker := range workers {
		if err := worker.Start(ctx); err != nil {
			rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lifecycleRollbackTimeout)
			rollbackErr := closeWorkers(rollbackCtx, started)
			cancel()
			application.finishFailedStart(rollbackErr == nil)
			return errors.Join(err, rollbackErr)
		}
		started = append(started, worker)
	}
	application.finishStart(true)
	return nil
}

// Close stops composed workers in reverse order.
func (application *Application) Close(ctx context.Context) error {
	application.mu.Lock()
	switch application.state {
	case lifecycleClosed:
		application.mu.Unlock()
		return nil
	case lifecycleStarting, lifecycleClosing:
		application.mu.Unlock()
		return ErrLifecycleTransition
	case lifecycleNew:
		application.state = lifecycleClosed
		application.mu.Unlock()
		return nil
	case lifecycleStarted:
		application.state = lifecycleClosing
	}
	workers := append([]kernel.WorkerFeature(nil), application.workers...)
	application.mu.Unlock()

	err := closeWorkers(ctx, workers)
	application.mu.Lock()
	application.state = lifecycleClosed
	application.mu.Unlock()
	return err
}

func (application *Application) finishStart(succeeded bool) {
	application.mu.Lock()
	defer application.mu.Unlock()
	if succeeded {
		application.state = lifecycleStarted
		return
	}
	application.state = lifecycleNew
}

// finishFailedStart publishes whether rollback proved that no worker remains
// active. A failed rollback poisons the Application closed: retrying Start when
// worker state is unknown could duplicate background workers or resources.
func (application *Application) finishFailedStart(rollbackSucceeded bool) {
	application.mu.Lock()
	defer application.mu.Unlock()
	if rollbackSucceeded {
		application.state = lifecycleNew
		return
	}
	application.state = lifecycleClosed
}

// Features returns the immutable composed feature descriptors.
func (application *Application) Features() []kernel.FeatureDescriptor {
	result := make([]kernel.FeatureDescriptor, 0, len(application.features))
	for _, feature := range application.features {
		result = append(result, cloneDescriptor(feature.Descriptor()))
	}
	return result
}

func inspectFeatures(features []kernel.Feature) (map[kernel.Capability]string, []kernel.WorkerFeature, error) {
	provided := make(map[kernel.Capability]string)
	workers := make([]kernel.WorkerFeature, 0)
	names := make(map[string]struct{})
	for _, feature := range features {
		descriptor, err := inspectFeature(feature, names)
		if err != nil {
			return nil, nil, err
		}
		if err = registerCapabilities(provided, descriptor); err != nil {
			return nil, nil, err
		}
		if worker, ok := feature.(kernel.WorkerFeature); ok {
			workers = append(workers, worker)
		}
	}
	return provided, workers, nil
}

func inspectFeature(feature kernel.Feature, names map[string]struct{}) (kernel.FeatureDescriptor, error) {
	if feature == nil {
		return kernel.FeatureDescriptor{}, ErrInvalidFeature
	}
	descriptor := feature.Descriptor()
	if descriptor.Name == "" {
		return kernel.FeatureDescriptor{}, ErrInvalidFeature
	}
	if _, duplicate := names[descriptor.Name]; duplicate {
		return kernel.FeatureDescriptor{}, ErrInvalidFeature
	}
	names[descriptor.Name] = struct{}{}
	return descriptor, nil
}

func registerCapabilities(provided map[kernel.Capability]string, descriptor kernel.FeatureDescriptor) error {
	for _, capability := range descriptor.Provides {
		if capability == "" {
			return ErrInvalidFeature
		}
		if _, duplicate := provided[capability]; duplicate {
			return ErrDuplicateCapability
		}
		provided[capability] = descriptor.Name
	}
	return nil
}

func validateRequirements(features []kernel.Feature, provided map[kernel.Capability]string) error {
	for _, feature := range features {
		for _, capability := range feature.Descriptor().Requires {
			if _, ok := provided[capability]; !ok {
				return ErrMissingCapability
			}
		}
	}
	return nil
}

func closeWorkers(ctx context.Context, workers []kernel.WorkerFeature) error {
	var result error
	for index := len(workers) - 1; index >= 0; index-- {
		result = errors.Join(result, workers[index].Close(ctx))
	}
	return result
}

func cloneDescriptor(descriptor kernel.FeatureDescriptor) kernel.FeatureDescriptor {
	descriptor.Requires = append([]kernel.Capability(nil), descriptor.Requires...)
	descriptor.Provides = append([]kernel.Capability(nil), descriptor.Provides...)
	return descriptor
}
