package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"strings"

	a2asdk "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
)

const (
	ProtocolVersion    = "1.0"
	maxAgentSkills     = 1024
	maxAgentExtensions = 256
	maxAgentSkillItems = 256
	maxRemoteTextBytes = 16 * 1024
	eventDiscovery     = "protocol.a2a.discovery"
	eventMessage       = "protocol.a2a.message"
	eventMessageStream = "protocol.a2a.message_stream"
	eventTaskSubscribe = "protocol.a2a.task_subscribe"
	eventTaskGet       = "protocol.a2a.task_get"
	eventTaskCancel    = "protocol.a2a.task_cancel"
)

var (
	ErrInvalidClient       = errors.New("invalid A2A client")
	ErrInvalidAgentCard    = errors.New("invalid A2A agent card")
	ErrUnsupportedProtocol = errors.New("unsupported A2A protocol")
	ErrInvalidMessage      = errors.New("invalid A2A message")
	ErrInvalidTask         = errors.New("invalid A2A task")
	ErrInvalidResult       = errors.New("invalid A2A result")
	ErrDiscoveryLimit      = errors.New("A2A discovery limit exceeded")
)

// ClientDependencies keep A2A protocol construction explicit.
type ClientDependencies struct {
	Transport *Transport
	Observers []plugin.Observer
}

func cloneStreamEvent(event StreamEvent) StreamEvent {
	event.Raw = append(json.RawMessage(nil), event.Raw...)
	if event.Task != nil {
		task := *event.Task
		task.Raw = append(json.RawMessage(nil), task.Raw...)
		event.Task = &task
	}
	if event.Message != nil {
		message := *event.Message
		message.Raw = append(json.RawMessage(nil), message.Raw...)
		event.Message = &message
	}
	if event.Artifact != nil {
		artifact := *event.Artifact
		artifact.Raw = append(json.RawMessage(nil), artifact.Raw...)
		event.Artifact = &artifact
	}
	return event
}

func projectStreamEvent(event a2asdk.Event) (StreamEvent, error) {
	if event == nil {
		return StreamEvent{}, ErrInvalidResult
	}
	switch value := event.(type) {
	case *a2asdk.Task:
		return projectTaskStreamEvent(value)
	case *a2asdk.Message:
		return projectMessageStreamEvent(value)
	case *a2asdk.TaskStatusUpdateEvent:
		return projectStatusStreamEvent(value)
	case *a2asdk.TaskArtifactUpdateEvent:
		return projectArtifactStreamEvent(value)
	default:
		return StreamEvent{}, fmt.Errorf("%w: %T", ErrInvalidResult, event)
	}
}

func projectTaskStreamEvent(task *a2asdk.Task) (StreamEvent, error) {
	projected, err := projectTask(task)
	if err != nil {
		return StreamEvent{}, err
	}
	return newStreamEvent(StreamEvent{Kind: StreamEventTask, Task: &projected}, task)
}

func projectMessageStreamEvent(message *a2asdk.Message) (StreamEvent, error) {
	projected, err := projectMessage(message)
	if err != nil {
		return StreamEvent{}, err
	}
	return newStreamEvent(StreamEvent{Kind: StreamEventMessage, Message: &projected}, message)
}

func projectStatusStreamEvent(event *a2asdk.TaskStatusUpdateEvent) (StreamEvent, error) {
	if event == nil {
		return StreamEvent{}, ErrInvalidTask
	}
	task, err := newTaskSnapshot(event.TaskID, event.ContextID, event.Status, event)
	if err != nil {
		return StreamEvent{}, err
	}
	return newStreamEvent(StreamEvent{Kind: StreamEventStatus, Task: &task}, event)
}

func projectArtifactStreamEvent(event *a2asdk.TaskArtifactUpdateEvent) (StreamEvent, error) {
	artifact, err := projectArtifactEvent(event)
	if err != nil {
		return StreamEvent{}, err
	}
	return newStreamEvent(StreamEvent{Kind: StreamEventArtifact, Artifact: &artifact}, event)
}

func newStreamEvent(projected StreamEvent, source any) (StreamEvent, error) {
	raw, err := json.Marshal(source)
	if err != nil {
		return StreamEvent{}, err
	}
	projected.Raw = append(json.RawMessage(nil), raw...)
	return cloneStreamEvent(projected), nil
}

func projectArtifactEvent(event *a2asdk.TaskArtifactUpdateEvent) (ArtifactSnapshot, error) {
	if event == nil || event.Artifact == nil || strings.TrimSpace(string(event.Artifact.ID)) == "" ||
		strings.TrimSpace(string(event.TaskID)) == "" || strings.TrimSpace(event.ContextID) == "" {
		return ArtifactSnapshot{}, ErrInvalidResult
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return ArtifactSnapshot{}, err
	}
	return ArtifactSnapshot{
		ID: strings.TrimSpace(string(event.Artifact.ID)), TaskID: strings.TrimSpace(string(event.TaskID)),
		ContextID: strings.TrimSpace(event.ContextID), Name: strings.TrimSpace(event.Artifact.Name),
		Append: event.Append, LastChunk: event.LastChunk, Raw: append(json.RawMessage(nil), raw...),
	}, nil
}

// StreamEventKind identifies one host-neutral A2A streaming event shape.
type StreamEventKind string

const (
	StreamEventTask     StreamEventKind = "task"
	StreamEventMessage  StreamEventKind = "message"
	StreamEventStatus   StreamEventKind = "status"
	StreamEventArtifact StreamEventKind = "artifact"
)

// ArtifactSnapshot is one isolated A2A artifact update projection.
type ArtifactSnapshot struct {
	ID        string
	TaskID    string
	ContextID string
	Name      string
	Append    bool
	LastChunk bool
	Raw       json.RawMessage
}

// StreamEvent keeps official A2A streaming types inside the edge module while
// preserving exact wire order and raw evidence for host observability.
type StreamEvent struct {
	Kind     StreamEventKind
	Task     *TaskSnapshot
	Message  *MessageSnapshot
	Artifact *ArtifactSnapshot
	Raw      json.RawMessage
}

// Client is the A2A v1 HTTP+JSON edge adapter.
type Client struct {
	transport *Transport
	observers *plugin.ObserverSet
}

// RemoteAgentSkill is the host-neutral projection of one Agent Card skill.
type RemoteAgentSkill struct {
	ID          string
	Name        string
	Description string
	Tags        []string
	Examples    []string
	InputModes  []string
	OutputModes []string
}

// Discovery is one immutable Agent Card projection with an explicitly selected
// A2A v1 HTTP+JSON interface.
type Discovery struct {
	Descriptor         RemoteAgentDescriptor
	DefaultInputModes  []string
	DefaultOutputModes []string
	Skills             []RemoteAgentSkill
	CapabilitiesJSON   json.RawMessage
}

// SendRequest carries the minimum host-neutral A2A message fields used by the
// Runtime remote-agent adapter.
type SendRequest struct {
	MessageID string
	ContextID string
	TaskID    string
	Text      string
}

// MessageSnapshot is one isolated remote A2A Message projection.
type MessageSnapshot struct {
	ID        string
	ContextID string
	TaskID    string
	Raw       json.RawMessage
}

// TaskSnapshot is one isolated remote A2A Task projection.
type TaskSnapshot struct {
	ID        string
	ContextID string
	State     string
	Terminal  bool
	Raw       json.RawMessage
}

// Interaction is the non-streaming SendMessage result, which is exactly one
// Message or Task in A2A v1.
type Interaction struct {
	Message *MessageSnapshot
	Task    *TaskSnapshot
	Raw     json.RawMessage
}

// NewClient constructs an A2A edge client without network defaults.
func NewClient(dependencies ClientDependencies) (*Client, error) {
	if dependencies.Transport == nil {
		return nil, ErrInvalidClient
	}
	observers, err := plugin.NewObserverSet(dependencies.Observers...)
	if err != nil {
		return nil, errors.Join(ErrInvalidClient, err)
	}
	return &Client{transport: dependencies.Transport, observers: observers}, nil
}

// Discover resolves the public Agent Card and selects only an exact v1
// HTTP+JSON interface. JSON-RPC/gRPC are never implicit fallbacks.
func (client *Client) Discover(ctx context.Context, rawBaseURL string) (Discovery, error) {
	client.observe(ctx, eventDiscovery, "started", false)
	discovery, err := client.discover(ctx, rawBaseURL)
	client.observeOutcome(ctx, eventDiscovery, err)
	return discovery, err
}

func (client *Client) discover(ctx context.Context, rawBaseURL string) (Discovery, error) {
	baseURL, httpClient, err := client.protocolHTTPClient(ctx, rawBaseURL)
	if err != nil {
		return Discovery{}, err
	}
	card, err := agentcard.NewResolver(httpClient).Resolve(ctx, baseURL)
	if err != nil {
		return Discovery{}, err
	}
	selected, err := selectHTTPJSONInterface(card)
	if err != nil {
		return Discovery{}, err
	}
	if _, _, err = client.protocolHTTPClient(ctx, selected.URL); err != nil {
		return Discovery{}, err
	}
	return projectDiscovery(card, selected)
}

// SendMessage sends one user text message using only the selected v1 HTTP+JSON interface.
func (client *Client) SendMessage(ctx context.Context, discovery Discovery, request SendRequest) (Interaction, error) {
	client.observe(ctx, eventMessage, "started", false)
	interaction, err := client.sendMessage(ctx, discovery, request)
	client.observeOutcome(ctx, eventMessage, err)
	return interaction, err
}

func (client *Client) sendMessage(ctx context.Context, discovery Discovery, request SendRequest) (Interaction, error) {
	message, err := newUserMessage(request)
	if err != nil {
		return Interaction{}, err
	}
	protocolClient, err := client.newProtocolClient(ctx, discovery.Descriptor)
	if err != nil {
		return Interaction{}, err
	}
	defer func() { _ = protocolClient.Destroy() }()
	result, err := protocolClient.SendMessage(ctx, &a2asdk.SendMessageRequest{
		Tenant: discovery.Descriptor.Tenant, Message: message,
	})
	if err != nil {
		return Interaction{}, err
	}
	return projectInteraction(result)
}

// SendStreamingMessage yields one host-neutral event for every official A2A
// streaming event without reordering, coalescing or implicit retries.
func (client *Client) SendStreamingMessage(
	ctx context.Context,
	discovery Discovery,
	request SendRequest,
) iter.Seq2[StreamEvent, error] {
	return func(yield func(StreamEvent, error) bool) {
		client.observe(ctx, eventMessageStream, "started", false)
		err := client.streamMessage(ctx, discovery, request, yield)
		client.observeOutcome(ctx, eventMessageStream, err)
	}
}

func (client *Client) streamMessage(
	ctx context.Context,
	discovery Discovery,
	request SendRequest,
	yield func(StreamEvent, error) bool,
) error {
	message, err := newUserMessage(request)
	if err != nil {
		yield(StreamEvent{}, err)
		return err
	}
	protocolClient, err := client.newProtocolClient(ctx, discovery.Descriptor)
	if err != nil {
		yield(StreamEvent{}, err)
		return err
	}
	defer func() { _ = protocolClient.Destroy() }()
	for event, streamErr := range protocolClient.SendStreamingMessage(ctx, &a2asdk.SendMessageRequest{
		Tenant: discovery.Descriptor.Tenant, Message: message,
	}) {
		if streamErr != nil {
			yield(StreamEvent{}, streamErr)
			return streamErr
		}
		projected, projectErr := projectStreamEvent(event)
		if projectErr != nil {
			yield(StreamEvent{}, projectErr)
			return projectErr
		}
		if !yield(projected, nil) {
			return nil
		}
	}
	return nil
}

// SubscribeToTask yields future events for one stable remote task.
func (client *Client) SubscribeToTask(
	ctx context.Context,
	discovery Discovery,
	taskID string,
) iter.Seq2[StreamEvent, error] {
	return func(yield func(StreamEvent, error) bool) {
		client.observe(ctx, eventTaskSubscribe, "started", false)
		err := client.subscribeTask(ctx, discovery, taskID, yield)
		client.observeOutcome(ctx, eventTaskSubscribe, err)
	}
}

func (client *Client) subscribeTask(
	ctx context.Context,
	discovery Discovery,
	taskID string,
	yield func(StreamEvent, error) bool,
) error {
	id := strings.TrimSpace(taskID)
	if id == "" {
		yield(StreamEvent{}, ErrInvalidTask)
		return ErrInvalidTask
	}
	protocolClient, err := client.newProtocolClient(ctx, discovery.Descriptor)
	if err != nil {
		yield(StreamEvent{}, err)
		return err
	}
	defer func() { _ = protocolClient.Destroy() }()
	for event, streamErr := range protocolClient.SubscribeToTask(ctx, &a2asdk.SubscribeToTaskRequest{
		Tenant: discovery.Descriptor.Tenant, ID: a2asdk.TaskID(id),
	}) {
		if streamErr != nil {
			yield(StreamEvent{}, streamErr)
			return streamErr
		}
		projected, projectErr := projectStreamEvent(event)
		if projectErr != nil {
			yield(StreamEvent{}, projectErr)
			return projectErr
		}
		if !yield(projected, nil) {
			return nil
		}
	}
	return nil
}

func newUserMessage(request SendRequest) (*a2asdk.Message, error) {
	messageID := strings.TrimSpace(request.MessageID)
	text := strings.TrimSpace(request.Text)
	if messageID == "" || text == "" {
		return nil, ErrInvalidMessage
	}
	message := a2asdk.NewMessage(a2asdk.MessageRoleUser, a2asdk.NewTextPart(text))
	message.ID = messageID
	message.ContextID = strings.TrimSpace(request.ContextID)
	message.TaskID = a2asdk.TaskID(strings.TrimSpace(request.TaskID))
	return message, nil
}

// GetTask reads one remote task by stable identity.
func (client *Client) GetTask(ctx context.Context, discovery Discovery, taskID string) (TaskSnapshot, error) {
	client.observe(ctx, eventTaskGet, "started", false)
	task, err := client.taskOperation(ctx, discovery, taskID, taskActionGet)
	client.observeOutcome(ctx, eventTaskGet, err)
	return task, err
}

// CancelTask asks the remote agent to cancel one task.
func (client *Client) CancelTask(ctx context.Context, discovery Discovery, taskID string) (TaskSnapshot, error) {
	client.observe(ctx, eventTaskCancel, "started", false)
	task, err := client.taskOperation(ctx, discovery, taskID, taskActionCancel)
	client.observeOutcome(ctx, eventTaskCancel, err)
	return task, err
}

type taskAction uint8

const (
	taskActionGet taskAction = iota + 1
	taskActionCancel
)

func (client *Client) taskOperation(
	ctx context.Context,
	discovery Discovery,
	taskID string,
	action taskAction,
) (TaskSnapshot, error) {
	id := strings.TrimSpace(taskID)
	if id == "" {
		return TaskSnapshot{}, ErrInvalidTask
	}
	protocolClient, err := client.newProtocolClient(ctx, discovery.Descriptor)
	if err != nil {
		return TaskSnapshot{}, err
	}
	defer func() { _ = protocolClient.Destroy() }()
	task, err := executeTaskAction(ctx, protocolClient, discovery.Descriptor.Tenant, a2asdk.TaskID(id), action)
	if err != nil {
		return TaskSnapshot{}, err
	}
	return projectTask(task)
}

func executeTaskAction(
	ctx context.Context,
	client *a2aclient.Client,
	tenant string,
	taskID a2asdk.TaskID,
	action taskAction,
) (*a2asdk.Task, error) {
	switch action {
	case taskActionGet:
		return client.GetTask(ctx, &a2asdk.GetTaskRequest{Tenant: tenant, ID: taskID})
	case taskActionCancel:
		return client.CancelTask(ctx, &a2asdk.CancelTaskRequest{Tenant: tenant, ID: taskID})
	default:
		return nil, ErrInvalidTask
	}
}

func (client *Client) newProtocolClient(
	ctx context.Context,
	descriptor RemoteAgentDescriptor,
) (*a2aclient.Client, error) {
	if strings.TrimSpace(descriptor.ProtocolVersion) != ProtocolVersion ||
		strings.TrimSpace(descriptor.ProtocolBinding) != string(a2asdk.TransportProtocolHTTPJSON) {
		return nil, ErrUnsupportedProtocol
	}
	endpoint, httpClient, err := client.protocolHTTPClient(ctx, descriptor.PreferredURL)
	if err != nil {
		return nil, err
	}
	interfaceDescriptor := &a2asdk.AgentInterface{
		URL: endpoint, ProtocolBinding: a2asdk.TransportProtocolHTTPJSON,
		ProtocolVersion: a2asdk.Version, Tenant: strings.TrimSpace(descriptor.Tenant),
	}
	return a2aclient.NewFromEndpoints(ctx, []*a2asdk.AgentInterface{interfaceDescriptor},
		a2aclient.WithDefaultsDisabled(),
		a2aclient.WithConfig(a2aclient.Config{
			PreferredTransports:      []a2asdk.TransportProtocol{a2asdk.TransportProtocolHTTPJSON},
			DisableTenantPropagation: true,
		}),
		a2aclient.WithRESTTransport(httpClient),
	)
}

func (client *Client) protocolHTTPClient(ctx context.Context, rawEndpoint string) (string, *http.Client, error) {
	if client == nil || client.transport == nil || client.transport.client == nil {
		return "", nil, ErrInvalidClient
	}
	endpoint, headers, err := client.transport.prepare(ctx, rawEndpoint)
	if err != nil {
		return "", nil, err
	}
	headers.Del("A2A-Version")
	headers.Del("A2A-Extensions")
	httpClient := *client.transport.client
	base := httpClient.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	httpClient.Transport = &versionedRoundTripper{next: base, headers: headers}
	return endpoint, &httpClient, nil
}

type versionedRoundTripper struct {
	next    http.RoundTripper
	headers http.Header
}

func (roundTripper *versionedRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if roundTripper == nil || roundTripper.next == nil || request == nil {
		return nil, ErrInvalidTransport
	}
	clone := request.Clone(request.Context())
	clone.Header = request.Header.Clone()
	for key, values := range roundTripper.headers {
		clone.Header.Del(key)
		for _, value := range values {
			clone.Header.Add(key, value)
		}
	}
	clone.Header.Set("A2A-Version", ProtocolVersion)
	return roundTripper.next.RoundTrip(clone)
}

func selectHTTPJSONInterface(card *a2asdk.AgentCard) (*a2asdk.AgentInterface, error) {
	if card == nil || strings.TrimSpace(card.Name) == "" {
		return nil, ErrInvalidAgentCard
	}
	for _, item := range card.SupportedInterfaces {
		if item != nil && item.ProtocolVersion == a2asdk.Version && item.ProtocolBinding == a2asdk.TransportProtocolHTTPJSON {
			return item, nil
		}
	}
	return nil, ErrUnsupportedProtocol
}

func projectDiscovery(card *a2asdk.AgentCard, selected *a2asdk.AgentInterface) (Discovery, error) {
	if card == nil || selected == nil {
		return Discovery{}, ErrInvalidAgentCard
	}
	if err := validateAgentCardProjection(card, selected); err != nil {
		return Discovery{}, err
	}
	capabilitiesJSON, err := json.Marshal(card.Capabilities)
	if err != nil {
		return Discovery{}, err
	}
	discovery := Discovery{
		Descriptor: RemoteAgentDescriptor{
			Name: strings.TrimSpace(card.Name), Description: strings.TrimSpace(card.Description),
			AgentVersion: strings.TrimSpace(card.Version), PreferredURL: strings.TrimSpace(selected.URL),
			ProtocolVersion: string(selected.ProtocolVersion), ProtocolBinding: string(selected.ProtocolBinding),
			Tenant: strings.TrimSpace(selected.Tenant), Capabilities: projectCapabilities(card.Capabilities),
		},
		DefaultInputModes:  append([]string(nil), card.DefaultInputModes...),
		DefaultOutputModes: append([]string(nil), card.DefaultOutputModes...),
		CapabilitiesJSON:   append(json.RawMessage(nil), capabilitiesJSON...),
		Skills:             make([]RemoteAgentSkill, 0, len(card.Skills)),
	}
	for _, skill := range card.Skills {
		projected, projectErr := projectSkill(skill)
		if projectErr != nil {
			return Discovery{}, projectErr
		}
		discovery.Skills = append(discovery.Skills, projected)
	}
	return cloneDiscovery(discovery), nil
}

func validateAgentCardProjection(card *a2asdk.AgentCard, selected *a2asdk.AgentInterface) error {
	if len(card.Skills) > maxAgentSkills || len(card.Capabilities.Extensions) > maxAgentExtensions {
		return ErrDiscoveryLimit
	}
	if !validAgentCardIdentity(card, selected) ||
		validateRemoteStrings(card.DefaultInputModes) != nil ||
		validateRemoteStrings(card.DefaultOutputModes) != nil {
		return ErrDiscoveryLimit
	}
	for _, extension := range card.Capabilities.Extensions {
		if !validRemoteText(extension.URI, true) {
			return ErrDiscoveryLimit
		}
	}
	return nil
}

func validAgentCardIdentity(card *a2asdk.AgentCard, selected *a2asdk.AgentInterface) bool {
	return validRemoteText(card.Name, true) && validRemoteText(card.Description, false) &&
		validRemoteText(card.Version, false) && validRemoteText(selected.URL, true) &&
		validRemoteText(selected.Tenant, false)
}

func projectSkill(skill a2asdk.AgentSkill) (RemoteAgentSkill, error) {
	if !validRemoteText(skill.ID, true) || !validRemoteText(skill.Name, true) || !validRemoteText(skill.Description, false) ||
		validateRemoteStrings(skill.Tags) != nil ||
		validateRemoteStrings(skill.Examples) != nil ||
		validateRemoteStrings(skill.InputModes) != nil ||
		validateRemoteStrings(skill.OutputModes) != nil {
		return RemoteAgentSkill{}, ErrDiscoveryLimit
	}
	return RemoteAgentSkill{
		ID: strings.TrimSpace(skill.ID), Name: strings.TrimSpace(skill.Name), Description: strings.TrimSpace(skill.Description),
		Tags: append([]string(nil), skill.Tags...), Examples: append([]string(nil), skill.Examples...),
		InputModes: append([]string(nil), skill.InputModes...), OutputModes: append([]string(nil), skill.OutputModes...),
	}, nil
}

func validateRemoteStrings(values []string) error {
	if len(values) > maxAgentSkillItems {
		return ErrDiscoveryLimit
	}
	for _, value := range values {
		if !validRemoteText(value, false) {
			return ErrDiscoveryLimit
		}
	}
	return nil
}

func validRemoteText(value string, required bool) bool {
	normalized := strings.TrimSpace(value)
	return (!required || normalized != "") && len(normalized) <= maxRemoteTextBytes
}

func projectCapabilities(capabilities a2asdk.AgentCapabilities) []string {
	result := make([]string, 0, 3+len(capabilities.Extensions))
	if capabilities.Streaming {
		result = append(result, "streaming")
	}
	if capabilities.PushNotifications {
		result = append(result, "push_notifications")
	}
	if capabilities.ExtendedAgentCard {
		result = append(result, "extended_agent_card")
	}
	for _, extension := range capabilities.Extensions {
		if uri := strings.TrimSpace(extension.URI); uri != "" {
			result = append(result, "extension:"+uri)
		}
	}
	return result
}

func projectInteraction(result a2asdk.SendMessageResult) (Interaction, error) {
	if result == nil {
		return Interaction{}, ErrInvalidResult
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return Interaction{}, err
	}
	interaction := Interaction{Raw: append(json.RawMessage(nil), raw...)}
	switch value := result.(type) {
	case *a2asdk.Message:
		message, projectErr := projectMessage(value)
		if projectErr != nil {
			return Interaction{}, projectErr
		}
		interaction.Message = &message
	case *a2asdk.Task:
		task, projectErr := projectTask(value)
		if projectErr != nil {
			return Interaction{}, projectErr
		}
		interaction.Task = &task
	default:
		return Interaction{}, fmt.Errorf("%w: %T", ErrInvalidResult, result)
	}
	return cloneInteraction(interaction), nil
}

func projectMessage(message *a2asdk.Message) (MessageSnapshot, error) {
	if message == nil || strings.TrimSpace(message.ID) == "" {
		return MessageSnapshot{}, ErrInvalidMessage
	}
	raw, err := json.Marshal(message)
	if err != nil {
		return MessageSnapshot{}, err
	}
	return MessageSnapshot{
		ID: strings.TrimSpace(message.ID), ContextID: strings.TrimSpace(message.ContextID),
		TaskID: strings.TrimSpace(string(message.TaskID)), Raw: append(json.RawMessage(nil), raw...),
	}, nil
}

func projectTask(task *a2asdk.Task) (TaskSnapshot, error) {
	if task == nil {
		return TaskSnapshot{}, ErrInvalidTask
	}
	return newTaskSnapshot(task.ID, task.ContextID, task.Status, task)
}

func newTaskSnapshot(
	id a2asdk.TaskID,
	contextID string,
	status a2asdk.TaskStatus,
	source any,
) (TaskSnapshot, error) {
	normalizedID := strings.TrimSpace(string(id))
	normalizedContextID := strings.TrimSpace(contextID)
	if normalizedID == "" || normalizedContextID == "" {
		return TaskSnapshot{}, ErrInvalidTask
	}
	raw, err := json.Marshal(source)
	if err != nil {
		return TaskSnapshot{}, err
	}
	return TaskSnapshot{
		ID: normalizedID, ContextID: normalizedContextID,
		State: string(status.State), Terminal: status.State.Terminal(), Raw: append(json.RawMessage(nil), raw...),
	}, nil
}

func cloneDiscovery(discovery Discovery) Discovery {
	discovery.Descriptor = CloneRemoteAgentDescriptor(discovery.Descriptor)
	discovery.DefaultInputModes = append([]string(nil), discovery.DefaultInputModes...)
	discovery.DefaultOutputModes = append([]string(nil), discovery.DefaultOutputModes...)
	discovery.CapabilitiesJSON = append(json.RawMessage(nil), discovery.CapabilitiesJSON...)
	discovery.Skills = append([]RemoteAgentSkill(nil), discovery.Skills...)
	for index := range discovery.Skills {
		discovery.Skills[index].Tags = append([]string(nil), discovery.Skills[index].Tags...)
		discovery.Skills[index].Examples = append([]string(nil), discovery.Skills[index].Examples...)
		discovery.Skills[index].InputModes = append([]string(nil), discovery.Skills[index].InputModes...)
		discovery.Skills[index].OutputModes = append([]string(nil), discovery.Skills[index].OutputModes...)
	}
	return discovery
}

func cloneInteraction(interaction Interaction) Interaction {
	interaction.Raw = append(json.RawMessage(nil), interaction.Raw...)
	if interaction.Message != nil {
		message := *interaction.Message
		message.Raw = append(json.RawMessage(nil), message.Raw...)
		interaction.Message = &message
	}
	if interaction.Task != nil {
		task := *interaction.Task
		task.Raw = append(json.RawMessage(nil), task.Raw...)
		interaction.Task = &task
	}
	return interaction
}

func (client *Client) observe(ctx context.Context, eventType, status string, terminal bool) {
	if client == nil || client.observers == nil {
		return
	}
	client.observers.Observe(ctx, plugin.Event{Type: eventType, Status: status, Terminal: terminal})
}

func (client *Client) observeOutcome(ctx context.Context, eventType string, err error) {
	status := "completed"
	if err != nil {
		status = "failed"
	}
	client.observe(ctx, eventType, status, true)
}
