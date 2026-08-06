package kernel

import "context"

// FeatureDescriptor declares capability dependencies without acting as a service locator.
type FeatureDescriptor struct {
	Name     string
	Requires []Capability
	Provides []Capability
}

// Feature is an explicitly constructed runtime capability module.
type Feature interface {
	Descriptor() FeatureDescriptor
}

// WorkerFeature owns background lifecycle only when explicitly composed by a host.
type WorkerFeature interface {
	Feature
	Start(context.Context) error
	Close(context.Context) error
}
