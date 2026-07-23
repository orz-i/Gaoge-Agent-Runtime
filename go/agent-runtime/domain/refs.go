package domain

// ActorRef identifies the tenant-scoped actor that owns a runtime operation.
type ActorRef struct {
	TenantID string
	ActorID  string
}

// ThreadRef identifies a host-owned conversation thread without exposing its database key.
type ThreadRef struct {
	Kind string
	ID   string
}

// ProjectionRef identifies a host-owned input or output projection.
type ProjectionRef struct {
	Kind string
	ID   string
}

// ResourceRef identifies an immutable or revisioned external resource.
type ResourceRef struct {
	Kind     string
	ID       string
	Revision string
}
