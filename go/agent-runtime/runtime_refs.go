package agentruntime

import "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"

const ResourceKindSkill = "skill"

// NewActorRef constructs the public SDK reference without exposing domain package ownership to adapters.
func NewActorRef(tenantID, actorID string) domain.ActorRef {
	return domain.ActorRef{TenantID: tenantID, ActorID: actorID}
}

// NewThreadRef constructs a host-neutral thread reference at the Runtime SDK boundary.
func NewThreadRef(kind, id string) domain.ThreadRef {
	return domain.ThreadRef{Kind: kind, ID: id}
}
