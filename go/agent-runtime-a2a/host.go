package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"net/http"
	"net/url"
	"strings"
	"time"

	a2asdk "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/limiter"
)

var (
	ErrInvalidHost          = errors.New("invalid A2A host")
	ErrInvalidHostedRequest = errors.New("invalid A2A hosted request")
	ErrInvalidHostedEvent   = errors.New("invalid A2A hosted event")
	errHostedSinkClosed     = errors.New("A2A hosted sink closed")
)

// HostedCard is the host-neutral public identity of one explicitly exposed
// local agent. PublicURL is configuration, not a listener owned by the edge.
type HostedCard struct {
	PublicURL            string
	Name                 string
	Description          string
	Version              string
	Tenant               string
	DefaultInputModes    []string
	DefaultOutputModes   []string
	Skills               []RemoteAgentSkill
	SecuritySchemes      []HostedSecurityScheme
	SecurityRequirements []HostedSecurityRequirement
	Signatures           []RemoteAgentCardSignature
}

// HostedRequest is the protocol-neutral projection passed to a local host
// adapter. The original A2A Message is retained only as immutable JSON.
type HostedRequest struct {
	TaskID      string
	ContextID   string
	Tenant      string
	Principal   HostedPrincipal
	Message     json.RawMessage
	MessageView MessageSnapshot
}

// HostedCancelRequest identifies one explicit remote cancellation request.
type HostedCancelRequest struct {
	TaskID    string
	ContextID string
	Tenant    string
	Principal HostedPrincipal
}

// HostedEventKind defines the small output vocabulary accepted from a hosted
// local agent without exposing official A2A SDK types.
type HostedEventKind string

const (
	HostedEventArtifact HostedEventKind = "artifact"
	HostedEventMessage  HostedEventKind = "message"
	HostedEventStatus   HostedEventKind = "status"
)

// HostedStatus is a host-neutral lifecycle request. The A2A edge owns the
// exact mapping to protocol TaskState values.
type HostedStatus string

const (
	HostedStatusWorking       HostedStatus = "working"
	HostedStatusInputRequired HostedStatus = "input_required"
	HostedStatusAuthRequired  HostedStatus = "auth_required"
	HostedStatusCompleted     HostedStatus = "completed"
	HostedStatusFailed        HostedStatus = "failed"
	HostedStatusRejected      HostedStatus = "rejected"
	HostedStatusCanceled      HostedStatus = "canceled"
)

// HostedEvent is one local agent output translated by the A2A edge.
type HostedEvent struct {
	Kind        HostedEventKind
	Status      HostedStatus
	MessageID   string
	Text        string
	Parts       []ContentPart
	Metadata    json.RawMessage
	ArtifactID  string
	Name        string
	Description string
	Append      bool
	LastChunk   bool
}

// HostedSink receives local outputs in exact call order.
type HostedSink func(HostedEvent) error

// HostedAgent is the only local execution capability required by the A2A
// server edge. Implementations remain independent of the official A2A SDK.
type HostedAgent interface {
	Execute(context.Context, HostedRequest, HostedSink) error
	Cancel(context.Context, HostedCancelRequest) error
}

// HostDependencies keep public exposure and execution explicit.
type HostDependencies struct {
	Card          HostedCard
	Agent         HostedAgent
	Authenticator HostedAuthenticator
	TaskStore     HostedTaskStore
	Policy        HostPolicy
}

// HostPolicy enables production-only fail-closed requirements and bounded execution.
type HostPolicy struct {
	Production              bool
	AgentInactivityTimeout  time.Duration
	MaxConcurrentExecutions int
}

// Host owns only a pre-built HTTP handler; it never opens a listener or
// registers itself into a product router.
type Host struct{ handler http.Handler }

// NewHost adapts one host-neutral local agent to the official A2A v1 server.
func NewHost(dependencies HostDependencies) (*Host, error) {
	if dependencies.Agent == nil || !validHostPolicy(dependencies) {
		return nil, ErrInvalidHost
	}
	card, err := buildHostedAgentCard(dependencies.Card)
	if err != nil {
		return nil, err
	}
	executor := hostedExecutor{agent: dependencies.Agent}
	options := []a2asrv.RequestHandlerOption{a2asrv.WithCapabilityChecks(&card.Capabilities)}
	if dependencies.Authenticator != nil {
		options = append(options, a2asrv.WithCallInterceptors(hostAuthenticationInterceptor{
			authenticator: dependencies.Authenticator, allowedTenant: strings.TrimSpace(dependencies.Card.Tenant),
		}))
	}
	if dependencies.TaskStore != nil {
		options = append(options, a2asrv.WithTaskStore(newProtocolTaskStore(dependencies.TaskStore)))
	}
	if dependencies.Policy.AgentInactivityTimeout > 0 {
		options = append(options, a2asrv.WithAgentInactivityTimeout(dependencies.Policy.AgentInactivityTimeout))
	}
	if dependencies.Policy.MaxConcurrentExecutions > 0 {
		options = append(options, a2asrv.WithConcurrencyConfig(limiter.ConcurrencyConfig{
			MaxExecutions: dependencies.Policy.MaxConcurrentExecutions,
		}))
	}
	requestHandler := a2asrv.NewHandler(executor, options...)
	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	protocolHandler := a2asrv.NewRESTHandler(requestHandler)
	if strings.TrimSpace(dependencies.Card.Tenant) != "" {
		protocolHandler = a2asrv.NewTenantRESTHandler("/{*}", requestHandler)
	}
	mux.Handle("/", protocolHandler)
	return &Host{handler: mux}, nil
}

func validHostPolicy(dependencies HostDependencies) bool {
	policy := dependencies.Policy
	if policy.AgentInactivityTimeout < 0 || policy.MaxConcurrentExecutions < 0 {
		return false
	}
	if !policy.Production {
		return true
	}
	publicURL, err := url.Parse(strings.TrimSpace(dependencies.Card.PublicURL))
	return err == nil && publicURL.Scheme == "https" && publicURL.Host != "" &&
		dependencies.Authenticator != nil && dependencies.TaskStore != nil &&
		len(dependencies.Card.SecuritySchemes) > 0 && len(dependencies.Card.SecurityRequirements) > 0
}

// Handler returns the explicit A2A HTTP surface for host-controlled mounting.
func (host *Host) Handler() http.Handler {
	if host == nil {
		return nil
	}
	return host.handler
}

type hostedExecutor struct{ agent HostedAgent }

func (executor hostedExecutor) Execute(
	ctx context.Context,
	execCtx *a2asrv.ExecutorContext,
) iter.Seq2[a2asdk.Event, error] {
	return func(yield func(a2asdk.Event, error) bool) {
		request, err := projectHostedRequest(ctx, execCtx)
		if err != nil {
			yield(nil, err)
			return
		}
		if !startHostedExecution(execCtx, yield) {
			return
		}
		terminal, err := executor.executeHostedAgent(ctx, execCtx, request, yield)
		if errors.Is(err, errHostedSinkClosed) {
			return
		}
		if err != nil {
			yield(a2asdk.NewStatusUpdateEvent(execCtx, a2asdk.TaskStateFailed, nil), nil)
			return
		}
		if !terminal {
			yield(a2asdk.NewStatusUpdateEvent(execCtx, a2asdk.TaskStateCompleted, nil), nil)
		}
	}
}

func startHostedExecution(execCtx *a2asrv.ExecutorContext, yield func(a2asdk.Event, error) bool) bool {
	if execCtx.StoredTask == nil && !yield(a2asdk.NewSubmittedTask(execCtx, execCtx.Message), nil) {
		return false
	}
	return yield(a2asdk.NewStatusUpdateEvent(execCtx, a2asdk.TaskStateWorking, nil), nil)
}

func (executor hostedExecutor) executeHostedAgent(
	ctx context.Context,
	execCtx *a2asrv.ExecutorContext,
	request HostedRequest,
	yield func(a2asdk.Event, error) bool,
) (bool, error) {
	terminal := false
	sink := func(event HostedEvent) error {
		protocolEvent, isTerminal, err := projectHostedOutput(execCtx, event)
		if err != nil {
			return err
		}
		terminal = terminal || isTerminal
		if !yield(protocolEvent, nil) {
			return errHostedSinkClosed
		}
		return nil
	}
	return terminal, executor.agent.Execute(ctx, request, sink)
}

func (executor hostedExecutor) Cancel(
	ctx context.Context,
	execCtx *a2asrv.ExecutorContext,
) iter.Seq2[a2asdk.Event, error] {
	return func(yield func(a2asdk.Event, error) bool) {
		request, err := projectHostedCancel(ctx, execCtx)
		if err != nil {
			yield(nil, err)
			return
		}
		if err = executor.agent.Cancel(ctx, request); err != nil {
			yield(nil, err)
			return
		}
		yield(a2asdk.NewStatusUpdateEvent(execCtx, a2asdk.TaskStateCanceled, nil), nil)
	}
}

func buildHostedAgentCard(card HostedCard) (*a2asdk.AgentCard, error) {
	publicURL := strings.TrimSpace(card.PublicURL)
	name := strings.TrimSpace(card.Name)
	version := strings.TrimSpace(card.Version)
	if !validRemoteText(publicURL, true) || !validRemoteText(name, true) || !validRemoteText(version, true) ||
		!validRemoteText(card.Description, false) || !validRemoteText(card.Tenant, false) ||
		validateRemoteStrings(card.DefaultInputModes) != nil ||
		validateRemoteStrings(card.DefaultOutputModes) != nil || len(card.Skills) > maxAgentSkills {
		return nil, ErrInvalidHost
	}
	skills := make([]a2asdk.AgentSkill, 0, len(card.Skills))
	for _, skill := range card.Skills {
		if _, err := projectSkill(a2asdk.AgentSkill{
			ID: skill.ID, Name: skill.Name, Description: skill.Description, Tags: skill.Tags,
			Examples: skill.Examples, InputModes: skill.InputModes, OutputModes: skill.OutputModes,
		}); err != nil {
			return nil, ErrInvalidHost
		}
		skills = append(skills, a2asdk.AgentSkill{
			ID: strings.TrimSpace(skill.ID), Name: strings.TrimSpace(skill.Name), Description: strings.TrimSpace(skill.Description),
			Tags: append([]string(nil), skill.Tags...), Examples: append([]string(nil), skill.Examples...),
			InputModes: append([]string(nil), skill.InputModes...), OutputModes: append([]string(nil), skill.OutputModes...),
		})
	}
	projected := &a2asdk.AgentCard{
		Name: name, Description: strings.TrimSpace(card.Description), Version: version,
		Capabilities:      a2asdk.AgentCapabilities{Streaming: true},
		DefaultInputModes: append([]string(nil), card.DefaultInputModes...), DefaultOutputModes: append([]string(nil), card.DefaultOutputModes...),
		SupportedInterfaces: []*a2asdk.AgentInterface{{
			URL: publicURL, ProtocolBinding: a2asdk.TransportProtocolHTTPJSON, ProtocolVersion: a2asdk.Version,
			Tenant: strings.TrimSpace(card.Tenant),
		}},
		Skills: skills,
	}
	if err := projectHostedSecurity(card, projected); err != nil {
		return nil, err
	}
	return projected, nil
}

func projectHostedRequest(ctx context.Context, execCtx *a2asrv.ExecutorContext) (HostedRequest, error) {
	if execCtx == nil || execCtx.Message == nil || strings.TrimSpace(string(execCtx.TaskID)) == "" ||
		strings.TrimSpace(execCtx.ContextID) == "" {
		return HostedRequest{}, ErrInvalidHostedRequest
	}
	raw, err := json.Marshal(execCtx.Message)
	if err != nil {
		return HostedRequest{}, err
	}
	messageView, err := projectMessage(execCtx.Message)
	if err != nil {
		return HostedRequest{}, err
	}
	return HostedRequest{
		TaskID: strings.TrimSpace(string(execCtx.TaskID)), ContextID: strings.TrimSpace(execCtx.ContextID),
		Tenant: strings.TrimSpace(execCtx.Tenant), Principal: hostedPrincipalFromContext(ctx),
		Message: append(json.RawMessage(nil), raw...), MessageView: messageView,
	}, nil
}

func projectHostedCancel(ctx context.Context, execCtx *a2asrv.ExecutorContext) (HostedCancelRequest, error) {
	if execCtx == nil || strings.TrimSpace(string(execCtx.TaskID)) == "" || strings.TrimSpace(execCtx.ContextID) == "" {
		return HostedCancelRequest{}, ErrInvalidHostedRequest
	}
	return HostedCancelRequest{
		TaskID: strings.TrimSpace(string(execCtx.TaskID)), ContextID: strings.TrimSpace(execCtx.ContextID),
		Tenant: strings.TrimSpace(execCtx.Tenant), Principal: hostedPrincipalFromContext(ctx),
	}, nil
}

func projectHostedOutput(execCtx *a2asrv.ExecutorContext, event HostedEvent) (a2asdk.Event, bool, error) {
	switch event.Kind {
	case HostedEventArtifact:
		projected, err := projectHostedArtifact(execCtx, event)
		return projected, false, err
	case HostedEventMessage:
		projected, err := projectHostedMessage(execCtx, event)
		return projected, true, err
	case HostedEventStatus:
		return projectHostedStatus(execCtx, event.Status)
	default:
		return nil, false, ErrInvalidHostedEvent
	}
}

func projectHostedArtifact(execCtx *a2asrv.ExecutorContext, event HostedEvent) (*a2asdk.TaskArtifactUpdateEvent, error) {
	if execCtx == nil || !validRemoteText(event.Name, false) || !validRemoteText(event.Description, false) {
		return nil, ErrInvalidHostedEvent
	}
	parts, err := projectHostedParts(event)
	if err != nil {
		return nil, err
	}
	var projected *a2asdk.TaskArtifactUpdateEvent
	id := strings.TrimSpace(event.ArtifactID)
	if id == "" || !event.Append {
		projected = a2asdk.NewArtifactEvent(execCtx, parts...)
		if id != "" {
			projected.Artifact.ID = a2asdk.ArtifactID(id)
		}
	} else {
		projected = a2asdk.NewArtifactUpdateEvent(execCtx, a2asdk.ArtifactID(id), parts...)
	}
	projected.Artifact.Name = strings.TrimSpace(event.Name)
	projected.Artifact.Description = strings.TrimSpace(event.Description)
	metadata, err := decodeMetadata(event.Metadata)
	if err != nil {
		return nil, ErrInvalidHostedEvent
	}
	projected.Artifact.Metadata = metadata
	projected.Append = event.Append
	projected.LastChunk = event.LastChunk
	return projected, nil
}

func projectHostedMessage(execCtx *a2asrv.ExecutorContext, event HostedEvent) (*a2asdk.TaskStatusUpdateEvent, error) {
	messageID := strings.TrimSpace(event.MessageID)
	if messageID == "" || execCtx == nil {
		return nil, ErrInvalidHostedEvent
	}
	parts, err := projectHostedParts(event)
	if err != nil {
		return nil, err
	}
	message := a2asdk.NewMessage(a2asdk.MessageRoleAgent, parts...)
	metadata, err := decodeMetadata(event.Metadata)
	if err != nil {
		return nil, ErrInvalidHostedEvent
	}
	message.Metadata = metadata
	message.ID = messageID
	message.TaskID = execCtx.TaskID
	message.ContextID = execCtx.ContextID
	return a2asdk.NewStatusUpdateEvent(execCtx, a2asdk.TaskStateCompleted, message), nil
}

func projectHostedStatus(execCtx *a2asrv.ExecutorContext, status HostedStatus) (a2asdk.Event, bool, error) {
	if execCtx == nil {
		return nil, false, ErrInvalidHostedEvent
	}
	switch status {
	case HostedStatusWorking:
		return a2asdk.NewStatusUpdateEvent(execCtx, a2asdk.TaskStateWorking, nil), false, nil
	case HostedStatusInputRequired:
		return a2asdk.NewStatusUpdateEvent(execCtx, a2asdk.TaskStateInputRequired, nil), true, nil
	case HostedStatusAuthRequired:
		return a2asdk.NewStatusUpdateEvent(execCtx, a2asdk.TaskStateAuthRequired, nil), true, nil
	case HostedStatusCompleted:
		return a2asdk.NewStatusUpdateEvent(execCtx, a2asdk.TaskStateCompleted, nil), true, nil
	case HostedStatusFailed:
		return a2asdk.NewStatusUpdateEvent(execCtx, a2asdk.TaskStateFailed, nil), true, nil
	case HostedStatusRejected:
		return a2asdk.NewStatusUpdateEvent(execCtx, a2asdk.TaskStateRejected, nil), true, nil
	case HostedStatusCanceled:
		return a2asdk.NewStatusUpdateEvent(execCtx, a2asdk.TaskStateCanceled, nil), true, nil
	default:
		return nil, false, ErrInvalidHostedEvent
	}
}

func projectHostedParts(event HostedEvent) ([]*a2asdk.Part, error) {
	if (len(event.Parts) > 0 && strings.TrimSpace(event.Text) != "") || len(event.Parts) > maxContentParts {
		return nil, ErrInvalidHostedEvent
	}
	if len(event.Parts) == 0 {
		text := strings.TrimSpace(event.Text)
		if text == "" {
			return nil, ErrInvalidHostedEvent
		}
		return []*a2asdk.Part{a2asdk.NewTextPart(text)}, nil
	}
	parts := make([]*a2asdk.Part, 0, len(event.Parts))
	for _, part := range event.Parts {
		projected, err := toProtocolPart(part)
		if err != nil {
			return nil, ErrInvalidHostedEvent
		}
		parts = append(parts, projected)
	}
	return parts, nil
}
