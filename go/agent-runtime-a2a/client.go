package a2a

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"sort"
	"strings"
	"time"

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
	maxSecuritySchemes = 64
	maxCardSignatures  = 64
	maxContentParts    = 256
	maxTaskHistory     = 2048
	maxTaskArtifacts   = 1024
	maxRawPartBytes    = 8 * 1024 * 1024
	maxMetadataBytes   = 1024 * 1024
	maxRemoteTextBytes = 16 * 1024
	eventDiscovery     = "protocol.a2a.discovery"
	eventMessage       = "protocol.a2a.message"
	eventMessageStream = "protocol.a2a.message_stream"
	eventTaskSubscribe = "protocol.a2a.task_subscribe"
	eventTaskGet       = "protocol.a2a.task_get"
	eventTaskList      = "protocol.a2a.task_list"
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
		cloneTaskSnapshotValue(&task)
		event.Task = &task
	}
	if event.Message != nil {
		message := *event.Message
		message = cloneMessageSnapshot(message)
		event.Message = &message
	}
	if event.Artifact != nil {
		artifact := *event.Artifact
		cloneArtifactSnapshot(&artifact)
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
	return projectArtifact(event.Artifact, string(event.TaskID), event.ContextID, event.Append, event.LastChunk, event)
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
	ID          string
	TaskID      string
	ContextID   string
	Name        string
	Description string
	Append      bool
	LastChunk   bool
	Parts       []ContentPart
	Metadata    json.RawMessage
	Raw         json.RawMessage
}

// ContentPartKind is the protocol-neutral A2A content discriminator.
type ContentPartKind string

const (
	ContentPartText ContentPartKind = "text"
	ContentPartRaw  ContentPartKind = "raw"
	ContentPartData ContentPartKind = "data"
	ContentPartURL  ContentPartKind = "url"
)

// ContentPart preserves rich A2A content without exposing official SDK types.
// Exactly one field selected by Kind is populated.
type ContentPart struct {
	Kind      ContentPartKind
	Text      string
	Raw       []byte
	Data      json.RawMessage
	URL       string
	Filename  string
	MediaType string
	Metadata  json.RawMessage
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

// RemoteSecurityScheme is a bounded host-neutral Agent Card security projection.
type RemoteSecurityScheme struct {
	Name        string
	Type        string
	DetailsJSON json.RawMessage
}

// RemoteAgentCardSignature exposes JWS material for a host-owned trust policy.
// The A2A edge does not implicitly trust or verify a card.
type RemoteAgentCardSignature struct {
	Protected string
	Signature string
}

// Discovery is one immutable Agent Card projection with an explicitly selected
// A2A v1 HTTP+JSON interface.
type Discovery struct {
	Descriptor               RemoteAgentDescriptor
	DefaultInputModes        []string
	DefaultOutputModes       []string
	Skills                   []RemoteAgentSkill
	CapabilitiesJSON         json.RawMessage
	SecuritySchemes          []RemoteSecurityScheme
	SecurityRequirementsJSON json.RawMessage
	Signatures               []RemoteAgentCardSignature
	CardJSON                 json.RawMessage
}

// SendRequest carries the minimum host-neutral A2A message fields used by the
// Runtime remote-agent adapter.
type SendRequest struct {
	MessageID           string
	ContextID           string
	TaskID              string
	Text                string
	Parts               []ContentPart
	AcceptedOutputModes []string
	ReturnImmediately   bool
	HistoryLength       *int
}

// MessageSnapshot is one isolated remote A2A Message projection.
type MessageSnapshot struct {
	ID               string
	ContextID        string
	TaskID           string
	Role             string
	Parts            []ContentPart
	Extensions       []string
	ReferenceTaskIDs []string
	Metadata         json.RawMessage
	Raw              json.RawMessage
}

// TaskSnapshot is one isolated remote A2A Task projection.
type TaskSnapshot struct {
	ID            string
	ContextID     string
	State         string
	Terminal      bool
	StatusMessage *MessageSnapshot
	History       []MessageSnapshot
	Artifacts     []ArtifactSnapshot
	Metadata      json.RawMessage
	Raw           json.RawMessage
}

// ListTasksRequest is the bounded host-neutral A2A tasks/list query.
type ListTasksRequest struct {
	ContextID            string
	State                string
	PageSize             int
	PageToken            string
	HistoryLength        *int
	StatusTimestampAfter *time.Time
	IncludeArtifacts     bool
}

// TaskPage is one isolated A2A tasks/list result page.
type TaskPage struct {
	Tasks         []TaskSnapshot
	TotalSize     int
	PageSize      int
	NextPageToken string
	Raw           json.RawMessage
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
	protocolRequest, err := newSendMessageRequest(discovery, request)
	if err != nil {
		return Interaction{}, err
	}
	protocolClient, err := client.newProtocolClient(ctx, discovery.Descriptor)
	if err != nil {
		return Interaction{}, err
	}
	defer func() { _ = protocolClient.Destroy() }()
	result, err := protocolClient.SendMessage(ctx, protocolRequest)
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
	protocolRequest, err := newSendMessageRequest(discovery, request)
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
	for event, streamErr := range protocolClient.SendStreamingMessage(ctx, protocolRequest) {
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
	if messageID == "" || len(request.Parts) > maxContentParts ||
		(len(request.Parts) > 0 && strings.TrimSpace(request.Text) != "") {
		return nil, ErrInvalidMessage
	}
	parts := make([]*a2asdk.Part, 0, max(1, len(request.Parts)))
	if len(request.Parts) == 0 {
		text := strings.TrimSpace(request.Text)
		if text == "" {
			return nil, ErrInvalidMessage
		}
		parts = append(parts, a2asdk.NewTextPart(text))
	} else {
		for _, part := range request.Parts {
			projected, err := toProtocolPart(part)
			if err != nil {
				return nil, err
			}
			parts = append(parts, projected)
		}
	}
	message := a2asdk.NewMessage(a2asdk.MessageRoleUser, parts...)
	message.ID = messageID
	message.ContextID = strings.TrimSpace(request.ContextID)
	message.TaskID = a2asdk.TaskID(strings.TrimSpace(request.TaskID))
	return message, nil
}

func newSendMessageRequest(discovery Discovery, request SendRequest) (*a2asdk.SendMessageRequest, error) {
	message, err := newUserMessage(request)
	if err != nil || validateRemoteStrings(request.AcceptedOutputModes) != nil {
		return nil, errors.Join(err, ErrInvalidMessage)
	}
	protocolRequest := &a2asdk.SendMessageRequest{Tenant: discovery.Descriptor.Tenant, Message: message}
	if len(request.AcceptedOutputModes) > 0 || request.ReturnImmediately || request.HistoryLength != nil {
		protocolRequest.Config = &a2asdk.SendMessageConfig{
			AcceptedOutputModes: append([]string(nil), request.AcceptedOutputModes...),
			ReturnImmediately:   request.ReturnImmediately,
			HistoryLength:       cloneInt(request.HistoryLength),
		}
	}
	return protocolRequest, nil
}

func toProtocolPart(part ContentPart) (*a2asdk.Part, error) {
	var projected *a2asdk.Part
	switch part.Kind {
	case ContentPartText:
		if !validRemoteText(part.Text, true) {
			return nil, ErrInvalidMessage
		}
		projected = a2asdk.NewTextPart(part.Text)
	case ContentPartRaw:
		if len(part.Raw) == 0 || len(part.Raw) > maxRawPartBytes {
			return nil, ErrInvalidMessage
		}
		projected = a2asdk.NewRawPart(append([]byte(nil), part.Raw...))
	case ContentPartData:
		if len(part.Data) == 0 || len(part.Data) > maxMetadataBytes || !json.Valid(part.Data) {
			return nil, ErrInvalidMessage
		}
		var data any
		if err := json.Unmarshal(part.Data, &data); err != nil {
			return nil, ErrInvalidMessage
		}
		projected = a2asdk.NewDataPart(data)
	case ContentPartURL:
		if !validRemoteText(part.URL, true) {
			return nil, ErrInvalidMessage
		}
		projected = a2asdk.NewFileURLPart(a2asdk.URL(strings.TrimSpace(part.URL)), strings.TrimSpace(part.MediaType))
	default:
		return nil, ErrInvalidMessage
	}
	if !validRemoteText(part.Filename, false) || !validRemoteText(part.MediaType, false) {
		return nil, ErrInvalidMessage
	}
	metadata, err := decodeMetadata(part.Metadata)
	if err != nil {
		return nil, ErrInvalidMessage
	}
	projected.Filename = strings.TrimSpace(part.Filename)
	projected.MediaType = strings.TrimSpace(part.MediaType)
	projected.Metadata = metadata
	return projected, nil
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
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

// ListTasks returns one bounded tasks/list page for the selected tenant.
func (client *Client) ListTasks(
	ctx context.Context,
	discovery Discovery,
	request ListTasksRequest,
) (TaskPage, error) {
	client.observe(ctx, eventTaskList, "started", false)
	page, err := client.listTasks(ctx, discovery, request)
	client.observeOutcome(ctx, eventTaskList, err)
	return page, err
}

func (client *Client) listTasks(
	ctx context.Context,
	discovery Discovery,
	request ListTasksRequest,
) (TaskPage, error) {
	if request.PageSize < 0 || request.PageSize > 100 || !validRemoteText(request.ContextID, false) ||
		!validRemoteText(request.PageToken, false) || !validTaskState(request.State) {
		return TaskPage{}, ErrInvalidTask
	}
	protocolClient, err := client.newProtocolClient(ctx, discovery.Descriptor)
	if err != nil {
		return TaskPage{}, err
	}
	defer func() { _ = protocolClient.Destroy() }()
	response, err := protocolClient.ListTasks(ctx, &a2asdk.ListTasksRequest{
		Tenant: discovery.Descriptor.Tenant, ContextID: strings.TrimSpace(request.ContextID),
		Status: a2asdk.TaskState(strings.TrimSpace(request.State)), PageSize: request.PageSize,
		PageToken: strings.TrimSpace(request.PageToken), HistoryLength: cloneInt(request.HistoryLength),
		StatusTimestampAfter: cloneTime(request.StatusTimestampAfter), IncludeArtifacts: request.IncludeArtifacts,
	})
	if err != nil {
		return TaskPage{}, err
	}
	return projectTaskPage(response)
}

func validTaskState(state string) bool {
	switch strings.TrimSpace(state) {
	case "", string(a2asdk.TaskStateAuthRequired), string(a2asdk.TaskStateCanceled),
		string(a2asdk.TaskStateCompleted), string(a2asdk.TaskStateFailed),
		string(a2asdk.TaskStateInputRequired), string(a2asdk.TaskStateRejected),
		string(a2asdk.TaskStateSubmitted), string(a2asdk.TaskStateWorking):
		return true
	default:
		return false
	}
}

func projectTaskPage(response *a2asdk.ListTasksResponse) (TaskPage, error) {
	if response == nil || len(response.Tasks) > maxTaskHistory || response.TotalSize < 0 || response.PageSize < 0 {
		return TaskPage{}, ErrInvalidTask
	}
	raw, err := json.Marshal(response)
	if err != nil {
		return TaskPage{}, err
	}
	page := TaskPage{
		Tasks: make([]TaskSnapshot, 0, len(response.Tasks)), TotalSize: response.TotalSize,
		PageSize: response.PageSize, NextPageToken: strings.TrimSpace(response.NextPageToken),
		Raw: append(json.RawMessage(nil), raw...),
	}
	for _, task := range response.Tasks {
		projected, projectErr := projectTask(task)
		if projectErr != nil {
			return TaskPage{}, projectErr
		}
		page.Tasks = append(page.Tasks, projected)
	}
	return cloneTaskPage(page), nil
}

func cloneTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	clone := *value
	return &clone
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
	securityRequirementsJSON, err := marshalBounded(card.SecurityRequirements)
	if err != nil {
		return Discovery{}, err
	}
	cardJSON, err := marshalBounded(card)
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
		DefaultInputModes:        append([]string(nil), card.DefaultInputModes...),
		DefaultOutputModes:       append([]string(nil), card.DefaultOutputModes...),
		CapabilitiesJSON:         append(json.RawMessage(nil), capabilitiesJSON...),
		Skills:                   make([]RemoteAgentSkill, 0, len(card.Skills)),
		SecuritySchemes:          make([]RemoteSecurityScheme, 0, len(card.SecuritySchemes)),
		SecurityRequirementsJSON: append(json.RawMessage(nil), securityRequirementsJSON...),
		Signatures:               make([]RemoteAgentCardSignature, 0, len(card.Signatures)),
		CardJSON:                 append(json.RawMessage(nil), cardJSON...),
	}
	securityNames := make([]string, 0, len(card.SecuritySchemes))
	for name := range card.SecuritySchemes {
		securityNames = append(securityNames, string(name))
	}
	sort.Strings(securityNames)
	for _, securityName := range securityNames {
		name := a2asdk.SecuritySchemeName(securityName)
		scheme := card.SecuritySchemes[name]
		projected, projectErr := projectSecurityScheme(name, scheme)
		if projectErr != nil {
			return Discovery{}, projectErr
		}
		discovery.SecuritySchemes = append(discovery.SecuritySchemes, projected)
	}
	for _, signature := range card.Signatures {
		if !validRemoteText(signature.Protected, true) || !validRemoteText(signature.Signature, true) {
			return Discovery{}, ErrDiscoveryLimit
		}
		discovery.Signatures = append(discovery.Signatures, RemoteAgentCardSignature{
			Protected: strings.TrimSpace(signature.Protected), Signature: strings.TrimSpace(signature.Signature),
		})
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
	if len(card.Skills) > maxAgentSkills || len(card.Capabilities.Extensions) > maxAgentExtensions ||
		len(card.SecuritySchemes) > maxSecuritySchemes || len(card.SecurityRequirements) > maxSecuritySchemes ||
		len(card.Signatures) > maxCardSignatures {
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

func projectSecurityScheme(name a2asdk.SecuritySchemeName, scheme a2asdk.SecurityScheme) (RemoteSecurityScheme, error) {
	projected := RemoteSecurityScheme{Name: strings.TrimSpace(string(name))}
	if !validRemoteText(projected.Name, true) || scheme == nil {
		return RemoteSecurityScheme{}, ErrDiscoveryLimit
	}
	switch scheme.(type) {
	case a2asdk.APIKeySecurityScheme:
		projected.Type = "api_key"
	case a2asdk.HTTPAuthSecurityScheme:
		projected.Type = "http"
	case a2asdk.MutualTLSSecurityScheme:
		projected.Type = "mutual_tls"
	case a2asdk.OAuth2SecurityScheme:
		projected.Type = "oauth2"
	case a2asdk.OpenIDConnectSecurityScheme:
		projected.Type = "open_id_connect"
	default:
		return RemoteSecurityScheme{}, ErrDiscoveryLimit
	}
	details, err := marshalBounded(scheme)
	if err != nil {
		return RemoteSecurityScheme{}, err
	}
	projected.DetailsJSON = details
	return projected, nil
}

func marshalBounded(value any) (json.RawMessage, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(raw) > maxMetadataBytes {
		return nil, ErrDiscoveryLimit
	}
	if string(raw) == "null" {
		return nil, nil
	}
	return append(json.RawMessage(nil), raw...), nil
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
	if message == nil || strings.TrimSpace(message.ID) == "" || len(message.Parts) == 0 ||
		len(message.Parts) > maxContentParts || validateRemoteStrings(message.Extensions) != nil ||
		len(message.ReferenceTasks) > maxTaskHistory {
		return MessageSnapshot{}, ErrInvalidMessage
	}
	raw, err := json.Marshal(message)
	if err != nil {
		return MessageSnapshot{}, err
	}
	metadata, err := marshalBounded(message.Metadata)
	if err != nil {
		return MessageSnapshot{}, err
	}
	projected := MessageSnapshot{
		ID: strings.TrimSpace(message.ID), ContextID: strings.TrimSpace(message.ContextID),
		TaskID: strings.TrimSpace(string(message.TaskID)), Role: string(message.Role),
		Parts: make([]ContentPart, 0, len(message.Parts)), Extensions: append([]string(nil), message.Extensions...),
		ReferenceTaskIDs: make([]string, 0, len(message.ReferenceTasks)), Metadata: metadata,
		Raw: append(json.RawMessage(nil), raw...),
	}
	for _, part := range message.Parts {
		content, projectErr := projectContentPart(part)
		if projectErr != nil {
			return MessageSnapshot{}, projectErr
		}
		projected.Parts = append(projected.Parts, content)
	}
	for _, taskID := range message.ReferenceTasks {
		if id := strings.TrimSpace(string(taskID)); id != "" {
			projected.ReferenceTaskIDs = append(projected.ReferenceTaskIDs, id)
		}
	}
	return cloneMessageSnapshot(projected), nil
}

func projectTask(task *a2asdk.Task) (TaskSnapshot, error) {
	if task == nil || len(task.History) > maxTaskHistory || len(task.Artifacts) > maxTaskArtifacts {
		return TaskSnapshot{}, ErrInvalidTask
	}
	projected, err := newTaskSnapshot(task.ID, task.ContextID, task.Status, task)
	if err != nil {
		return TaskSnapshot{}, err
	}
	metadata, err := marshalBounded(task.Metadata)
	if err != nil {
		return TaskSnapshot{}, err
	}
	projected.Metadata = metadata
	projected.History = make([]MessageSnapshot, 0, len(task.History))
	for _, message := range task.History {
		historyItem, projectErr := projectMessage(message)
		if projectErr != nil {
			return TaskSnapshot{}, projectErr
		}
		projected.History = append(projected.History, historyItem)
	}
	projected.Artifacts = make([]ArtifactSnapshot, 0, len(task.Artifacts))
	for _, artifact := range task.Artifacts {
		artifactItem, projectErr := projectArtifact(artifact, string(task.ID), task.ContextID, false, true, artifact)
		if projectErr != nil {
			return TaskSnapshot{}, projectErr
		}
		projected.Artifacts = append(projected.Artifacts, artifactItem)
	}
	cloneTaskSnapshotValue(&projected)
	return projected, nil
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
	projected := TaskSnapshot{
		ID: normalizedID, ContextID: normalizedContextID,
		State: string(status.State), Terminal: status.State.Terminal(), Raw: append(json.RawMessage(nil), raw...),
	}
	if status.Message != nil {
		message, projectErr := projectMessage(status.Message)
		if projectErr != nil {
			return TaskSnapshot{}, projectErr
		}
		projected.StatusMessage = &message
	}
	return projected, nil
}

func projectArtifact(
	artifact *a2asdk.Artifact,
	taskID string,
	contextID string,
	appendContent bool,
	lastChunk bool,
	source any,
) (ArtifactSnapshot, error) {
	if artifact == nil || strings.TrimSpace(string(artifact.ID)) == "" || len(artifact.Parts) == 0 ||
		len(artifact.Parts) > maxContentParts || validateRemoteStrings(artifact.Extensions) != nil {
		return ArtifactSnapshot{}, ErrInvalidResult
	}
	raw, err := json.Marshal(source)
	if err != nil {
		return ArtifactSnapshot{}, err
	}
	metadata, err := marshalBounded(artifact.Metadata)
	if err != nil {
		return ArtifactSnapshot{}, err
	}
	projected := ArtifactSnapshot{
		ID: strings.TrimSpace(string(artifact.ID)), TaskID: strings.TrimSpace(taskID),
		ContextID: strings.TrimSpace(contextID), Name: strings.TrimSpace(artifact.Name),
		Description: strings.TrimSpace(artifact.Description), Append: appendContent, LastChunk: lastChunk,
		Parts: make([]ContentPart, 0, len(artifact.Parts)), Metadata: metadata,
		Raw: append(json.RawMessage(nil), raw...),
	}
	for _, part := range artifact.Parts {
		content, projectErr := projectContentPart(part)
		if projectErr != nil {
			return ArtifactSnapshot{}, projectErr
		}
		projected.Parts = append(projected.Parts, content)
	}
	cloneArtifactSnapshot(&projected)
	return projected, nil
}

func projectContentPart(part *a2asdk.Part) (ContentPart, error) {
	if part == nil || !validRemoteText(part.Filename, false) || !validRemoteText(part.MediaType, false) {
		return ContentPart{}, ErrInvalidResult
	}
	metadata, err := marshalBounded(part.Metadata)
	if err != nil {
		return ContentPart{}, err
	}
	projected := ContentPart{
		Filename: strings.TrimSpace(part.Filename), MediaType: strings.TrimSpace(part.MediaType), Metadata: metadata,
	}
	switch content := part.Content.(type) {
	case a2asdk.Text:
		if !validRemoteText(string(content), false) {
			return ContentPart{}, ErrInvalidResult
		}
		projected.Kind, projected.Text = ContentPartText, string(content)
	case a2asdk.Raw:
		if len(content) > maxRawPartBytes {
			return ContentPart{}, ErrInvalidResult
		}
		projected.Kind, projected.Raw = ContentPartRaw, append([]byte(nil), content...)
	case a2asdk.Data:
		data, marshalErr := marshalBounded(content.Value)
		if marshalErr != nil {
			return ContentPart{}, marshalErr
		}
		projected.Kind, projected.Data = ContentPartData, data
	case a2asdk.URL:
		if !validRemoteText(string(content), true) {
			return ContentPart{}, ErrInvalidResult
		}
		projected.Kind, projected.URL = ContentPartURL, strings.TrimSpace(string(content))
	default:
		return ContentPart{}, ErrInvalidResult
	}
	return cloneContentPart(projected), nil
}

func cloneDiscovery(discovery Discovery) Discovery {
	discovery.Descriptor = CloneRemoteAgentDescriptor(discovery.Descriptor)
	discovery.DefaultInputModes = append([]string(nil), discovery.DefaultInputModes...)
	discovery.DefaultOutputModes = append([]string(nil), discovery.DefaultOutputModes...)
	discovery.CapabilitiesJSON = append(json.RawMessage(nil), discovery.CapabilitiesJSON...)
	discovery.SecurityRequirementsJSON = append(json.RawMessage(nil), discovery.SecurityRequirementsJSON...)
	discovery.CardJSON = append(json.RawMessage(nil), discovery.CardJSON...)
	discovery.SecuritySchemes = append([]RemoteSecurityScheme(nil), discovery.SecuritySchemes...)
	for index := range discovery.SecuritySchemes {
		discovery.SecuritySchemes[index].DetailsJSON = append(
			json.RawMessage(nil), discovery.SecuritySchemes[index].DetailsJSON...,
		)
	}
	discovery.Signatures = append([]RemoteAgentCardSignature(nil), discovery.Signatures...)
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
		message := cloneMessageSnapshot(*interaction.Message)
		interaction.Message = &message
	}
	if interaction.Task != nil {
		task := *interaction.Task
		cloneTaskSnapshotValue(&task)
		interaction.Task = &task
	}
	return interaction
}

func cloneMessageSnapshot(message MessageSnapshot) MessageSnapshot {
	message.Raw = append(json.RawMessage(nil), message.Raw...)
	message.Metadata = append(json.RawMessage(nil), message.Metadata...)
	message.Extensions = append([]string(nil), message.Extensions...)
	message.ReferenceTaskIDs = append([]string(nil), message.ReferenceTaskIDs...)
	message.Parts = append([]ContentPart(nil), message.Parts...)
	for index := range message.Parts {
		message.Parts[index] = cloneContentPart(message.Parts[index])
	}
	return message
}

func cloneTaskSnapshotValue(task *TaskSnapshot) {
	if task == nil {
		return
	}
	task.Raw = append(json.RawMessage(nil), task.Raw...)
	task.Metadata = append(json.RawMessage(nil), task.Metadata...)
	if task.StatusMessage != nil {
		message := cloneMessageSnapshot(*task.StatusMessage)
		task.StatusMessage = &message
	}
	task.History = append([]MessageSnapshot(nil), task.History...)
	for index := range task.History {
		task.History[index] = cloneMessageSnapshot(task.History[index])
	}
	task.Artifacts = append([]ArtifactSnapshot(nil), task.Artifacts...)
	for index := range task.Artifacts {
		cloneArtifactSnapshot(&task.Artifacts[index])
	}
}

func cloneArtifactSnapshot(artifact *ArtifactSnapshot) {
	if artifact == nil {
		return
	}
	artifact.Raw = append(json.RawMessage(nil), artifact.Raw...)
	artifact.Metadata = append(json.RawMessage(nil), artifact.Metadata...)
	artifact.Parts = append([]ContentPart(nil), artifact.Parts...)
	for index := range artifact.Parts {
		artifact.Parts[index] = cloneContentPart(artifact.Parts[index])
	}
}

func cloneContentPart(part ContentPart) ContentPart {
	part.Raw = append([]byte(nil), part.Raw...)
	part.Data = append(json.RawMessage(nil), part.Data...)
	part.Metadata = append(json.RawMessage(nil), part.Metadata...)
	return part
}

func cloneTaskPage(page TaskPage) TaskPage {
	page.Raw = append(json.RawMessage(nil), page.Raw...)
	page.Tasks = append([]TaskSnapshot(nil), page.Tasks...)
	for index := range page.Tasks {
		cloneTaskSnapshotValue(&page.Tasks[index])
	}
	return page
}

func decodeMetadata(raw json.RawMessage) (map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	if len(raw) > maxMetadataBytes || !json.Valid(raw) {
		return nil, ErrInvalidMessage
	}
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil || metadata == nil {
		return nil, ErrInvalidMessage
	}
	return metadata, nil
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
