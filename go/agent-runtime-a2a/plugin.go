package a2a

import (
	"context"
	"errors"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/handoff"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
)

const (
	// CapabilityPlugin is provided by one explicitly composed A2A edge plugin.
	CapabilityPlugin kernel.Capability = "protocol.a2a"
	TargetPrefix     string            = "a2a:"
)

var (
	ErrInvalidPlugin  = errors.New("invalid A2A plugin")
	ErrInvalidTarget  = errors.New("invalid A2A delegation target")
	ErrBindingMissing = errors.New("A2A remote binding is unavailable")
)

// Binding is one immutable, non-secret remote configuration revision. The
// host-provided Client resolves request-scoped credentials outside this value.
type Binding struct {
	TargetID  string
	Revision  string
	Discovery Discovery
	Client    RemoteClient
}

// BindingResolver resolves either the active revision (empty revision) or the
// exact revision already frozen into a durable shadow Run.
type BindingResolver interface {
	ResolveBinding(context.Context, string, string) (Binding, error)
}

// BindingResolverFunc adapts one product-owned remote Agent directory.
type BindingResolverFunc func(context.Context, string, string) (Binding, error)

func (resolver BindingResolverFunc) ResolveBinding(
	ctx context.Context,
	targetID string,
	revision string,
) (Binding, error) {
	if resolver == nil {
		return Binding{}, ErrBindingMissing
	}
	return resolver(ctx, targetID, revision)
}

// PluginDependencies keep the A2A edge statically composed and host-owned.
type PluginDependencies struct {
	Runtime  *kernel.Runtime
	Bindings BindingResolver
}

// Plugin is the typed A2A Feature and Handoff routing adapter. It does not
// discover code, open listeners, or register itself globally.
type Plugin struct {
	runtime  *kernel.Runtime
	bindings BindingResolver
}

// NewPlugin constructs one explicit A2A edge plugin.
func NewPlugin(dependencies PluginDependencies) (*Plugin, error) {
	if dependencies.Runtime == nil || dependencies.Bindings == nil {
		return nil, ErrInvalidPlugin
	}
	return &Plugin{runtime: dependencies.Runtime, bindings: dependencies.Bindings}, nil
}

// Descriptor declares the static microkernel dependency direction.
func (*Plugin) Descriptor() kernel.FeatureDescriptor {
	return kernel.FeatureDescriptor{
		Name: "a2a", Requires: []kernel.Capability{kernel.CapabilityRuntime},
		Provides: []kernel.Capability{CapabilityPlugin},
	}
}

// ResolveChild implements Handoff routing only for a2a:<public-id> members.
func (plugin *Plugin) ResolveChild(
	ctx context.Context,
	delegation handoff.Delegation,
) (handoff.ChildRunner, error) {
	if plugin == nil || plugin.runtime == nil || plugin.bindings == nil {
		return nil, ErrInvalidPlugin
	}
	targetID, ok := ParseTargetMemberID(delegation.MemberID)
	if !ok || strings.TrimSpace(delegation.ChildRunID) == "" {
		return nil, ErrInvalidTarget
	}
	revision, err := plugin.frozenRevision(ctx, delegation.ChildRunID, targetID)
	if err != nil {
		return nil, err
	}
	binding, err := plugin.bindings.ResolveBinding(ctx, targetID, revision)
	if err != nil {
		return nil, errors.Join(ErrBindingMissing, err)
	}
	if !validBinding(binding, targetID, revision) {
		return nil, ErrBindingMissing
	}
	return NewShadowRunner(ShadowDependencies{
		Runtime: plugin.runtime, Client: binding.Client, Discovery: binding.Discovery,
		TargetID: targetID, TargetRevision: binding.Revision,
	})
}

func (plugin *Plugin) frozenRevision(ctx context.Context, runID, targetID string) (string, error) {
	snapshot, err := plugin.runtime.Load(ctx, runID)
	if errors.Is(err, kernel.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if snapshot.Run.Kind != RunKind {
		return "", ErrInvalidTarget
	}
	state, err := decodeTopologyState(snapshot.State)
	if err != nil || state.TargetID != targetID || strings.TrimSpace(state.TargetRevision) == "" {
		return "", ErrRemoteBindingChanged
	}
	return state.TargetRevision, nil
}

// TargetMemberID creates the only product routing identity owned by this edge.
func TargetMemberID(targetID string) (string, error) {
	targetID = strings.TrimSpace(targetID)
	if !validTargetID(targetID) {
		return "", ErrInvalidTarget
	}
	return TargetPrefix + targetID, nil
}

// ParseTargetMemberID recognizes an A2A member without consulting a registry.
func ParseTargetMemberID(memberID string) (string, bool) {
	memberID = strings.TrimSpace(memberID)
	if !strings.HasPrefix(memberID, TargetPrefix) {
		return "", false
	}
	targetID := strings.TrimPrefix(memberID, TargetPrefix)
	return targetID, validTargetID(targetID)
}

func validTargetID(targetID string) bool {
	if targetID == "" || len(targetID) > 128 {
		return false
	}
	for _, character := range targetID {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func validBinding(binding Binding, targetID, revision string) bool {
	descriptor := binding.Discovery.Descriptor
	return binding.Client != nil && strings.TrimSpace(binding.TargetID) == targetID &&
		strings.TrimSpace(binding.Revision) != "" && (revision == "" || binding.Revision == revision) &&
		strings.TrimSpace(descriptor.Name) != "" && strings.TrimSpace(descriptor.PreferredURL) != "" &&
		descriptor.ProtocolVersion == ProtocolVersion
}

var (
	_ kernel.Feature              = (*Plugin)(nil)
	_ handoff.ChildRunnerResolver = (*Plugin)(nil)
)
