package agentruntime

import "context"

import "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"

// ActorSettingsSource supplies actor-scoped settings consumed by Agent Runtime.
type ActorSettingsSource interface {
	GetActorSettingValue(ctx context.Context, actor domain.ActorRef, key string) (string, error)
}
