package compose

import (
	"context"
	"errors"
	"sync"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/kernel"
)

var (
	ErrInvalidFeature      = errors.New("invalid runtime feature")
	ErrDuplicateCapability = errors.New("duplicate runtime capability")
	ErrMissingCapability   = errors.New("missing runtime capability")
	ErrAlreadyStarted      = errors.New("runtime application already started")
	ErrClosed              = errors.New("runtime application is closed")
)

// Application validates capabilities and owns only explicitly composed workers.
type Application struct {
	features []kernel.Feature
	workers  []kernel.WorkerFeature
	mu       sync.Mutex
	started  bool
	closed   bool
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
	defer application.mu.Unlock()
	if application.closed {
		return ErrClosed
	}
	if application.started {
		return ErrAlreadyStarted
	}
	started := make([]kernel.WorkerFeature, 0, len(application.workers))
	for _, worker := range application.workers {
		if err := worker.Start(ctx); err != nil {
			return errors.Join(err, closeWorkers(ctx, started))
		}
		started = append(started, worker)
	}
	application.started = true
	return nil
}

// Close stops composed workers in reverse order.
func (application *Application) Close(ctx context.Context) error {
	application.mu.Lock()
	defer application.mu.Unlock()
	if application.closed {
		return nil
	}
	application.closed = true
	if !application.started {
		return nil
	}
	return closeWorkers(ctx, application.workers)
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
