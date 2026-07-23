// Package agentruntime owns Agent Runtime use cases and policy.
package agentruntime

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	// semanticRecallDeadline：语义召回截止时限，超时后优雅跳过，不阻塞 LLM 关键路径。
	semanticRecallDeadline = 200 * time.Millisecond
)

const (
	// LLMTaskTypeText 表示统一文本运行任务。
	LLMTaskTypeText = "text"
)

type LLMRouteScope string

const (
	LLMRouteScopeUser     LLMRouteScope = "user"
	LLMRouteScopeInternal LLMRouteScope = "internal"
)

// LLMRouteInput 是 conversation 对 LLM Gateway 的路由准备请求。
type LLMRouteInput struct {
	PlatformModelName string
	TaskType          string
	Scope             LLMRouteScope
	Actor             domain.ActorRef
	Thread            domain.ThreadRef
	RequestID         string
}

// LLMRoute 是 Gateway 返回给 conversation 的只读路由描述，不携带 API key。
type LLMRoute struct {
	RouteID                     uint
	PlatformModelID             uint
	PlatformModelName           string
	UpstreamModelID             uint
	UpstreamID                  uint
	UpstreamName                string
	BindingCode                 string
	Protocol                    string
	Endpoint                    string
	ModelVendor                 string
	ModelIcon                   string
	ModelCapabilitiesJSON       string
	ModelSystemPrompt           string
	UpstreamModel               string
	ReasoningContentPassback    bool
	PreviousResponseIDSupported bool
	Request                     LLMRouteInput
}

// TextModelGateway is the Agent Runtime text-generation boundary.
type TextModelGateway interface {
	PrepareTextRoute(ctx context.Context, input LLMRouteInput) (*LLMRoute, error)
	PrepareDefaultTextRoute(ctx context.Context, input LLMRouteInput) (*LLMRoute, error)
	GenerateText(ctx context.Context, route *LLMRoute, input GenerateInput) (*GenerateOutput, error)
	GenerateTextStream(ctx context.Context, route *LLMRoute, input GenerateInput, onEvent func(GenerateStreamEvent) error) (*GenerateOutput, error)
}

func routeEndpoint(route *LLMRoute) string {
	if route == nil {
		return ""
	}
	if endpoint := strings.TrimSpace(route.Endpoint); endpoint != "" {
		return endpoint
	}
	return DefaultEndpointForAdapter(route.Protocol)
}

type basicServiceBillingContextKey struct{}

type basicServiceBillingContext struct {
	Actor  domain.ActorRef
	Thread domain.ThreadRef
}

// Engine owns Agent Runtime orchestration and durable runtime facts.
type Engine struct {
	cfg                   ConfigProvider
	repo                  Store
	threadContext         ThreadContextSource
	projectionContent     ProjectionContentSource
	turnProjections       TurnProjectionWriter
	attachments           AttachmentResolver
	settingsRepo          ActorSettingsSource
	cache                 GenerationStreamCacheRepository
	llmGateway            TextModelGateway
	memoryRecorder        Memory
	toolCatalog           ToolCatalog
	toolExecutor          ToolExecutor
	workspaces            WorkspaceRegistry
	embeddingSvc          EmbeddingPort
	ragSvc                RetrievalPort
	skillResolver         SkillResolver
	environmentProfiles   EnvironmentProfileResolver
	unitOfWork            UnitOfWork
	billingSvc            Billing
	auditWriter           AuditWriter
	logger                Logger
	tracer                Tracer
	toolLimiters          sync.Map
	generationStreams     *generationStreamRegistry
	continuationScheduler ContinuationScheduler
	runQueueWake          chan struct{}
	userMemCache          sync.Map // actor ref key → cached memories
	userSettingCache      sync.Map // actor ref + key → cached setting
	lifecycleMu           sync.Mutex
	workerCancel          context.CancelFunc
	workerWG              sync.WaitGroup
	started               bool
	closed                bool
}

func (s *Engine) llmAttribution() (string, string) {
	if s == nil || s.cfg == nil {
		return "", ""
	}
	cfg := s.cfg.Snapshot()
	return cfg.Attribution.PublicWebBaseURL, cfg.Attribution.AppName
}

// AttachmentInput 是消息附件入参（应用层内部传递，无序列化标签）。
type AttachmentInput struct {
	FileID                 string
	Kind                   string
	FileName               string
	MimeType               string
	DetectedMIME           string
	FileCategory           string
	FileSize               int64
	SHA256                 string
	MetaJSON               string
	PageCount              int
	ProcessingStatus       string
	ProcessingReady        bool
	ProcessingErrorCode    string
	ProcessingErrorMessage string
	ExtractStatus          string
	EmbedStatus            string
	ExtractedText          string
	RagOptOut              bool // 用户是否关闭该文件的 RAG；RAG 段直接复用，无需重查 DB
	ChunkCount             int  // 向量分块数；RAG 缓存 key 需要
	Current                bool // 是否为本轮用户显式上传的附件
	ContextMode            string
}

// RuntimeInput is the neutral prompt/context input shared by the text runtime.
type RuntimeInput struct {
	Actor                   domain.ActorRef
	Thread                  domain.ThreadRef
	InputProjection         domain.ProjectionRef
	OutputProjection        domain.ProjectionRef
	RequestID               string
	ContentType             string
	Content                 string
	PlatformModelName       string
	Options                 map[string]interface{}
	ClientRunID             string
	FileIDs                 []string
	SelectedToolKeys        []string
	SkillRefs               []domain.ResourceRef
	HTMLVisualPromptEnabled bool
	HTMLVisualColorMode     string
	ParentProjection        *domain.ProjectionRef
	SourceProjection        *domain.ProjectionRef
	BranchReason            string
	Instructions            string
	MemoryEnabled           bool
	Cancelable              bool
	// OnEvent 用于向调用方推送中间事件（如 rag_search），流式场景使用。
	OnEvent func(eventType string, payload map[string]interface{}) error
}

// TextRunExecutionLimits freezes tool-loop controls when a Text Run is created.
type TextRunExecutionLimits struct {
	MaxLLMCalls, MaxToolCalls       int
	ToolRetryCount, ToolConcurrency int
}

// RunMessageResult 返回用户消息与 AI 消息。
type RunMessageResult struct {
	Projection          TurnProjection
	MetadataRefreshHint string
	Billable            bool
	UpstreamID          uint
	UpstreamName        string
	PlatformModelName   string
	RoutedBindingCode   string
	UpstreamModelName   string
	UpstreamProtocol    string
	EffectiveOptions    map[string]interface{}
	UsageSpeed          string
	UsageServiceTier    string
	BillingRateClass    string
	RawUsageJSON        string
	CacheWrite5mTokens  int64
	CacheWrite1hTokens  int64
	ServerSideToolUsage map[string]int64
	ServiceItems        []ServiceUsageInput
	LatencyMS           int64
	StartedAt           time.Time
}

// New creates an Engine with one immutable dependency graph. It never starts
// background workers; hosts must call Start explicitly.
func New(cfg ConfigProvider, deps Dependencies) (*Engine, error) {
	if cfg == nil {
		return nil, fmt.Errorf("%w: config", ErrMissingDependency)
	}
	if deps.Store == nil {
		return nil, fmt.Errorf("%w: store", ErrMissingDependency)
	}
	if deps.Cache == nil {
		return nil, fmt.Errorf("%w: generation stream cache", ErrMissingDependency)
	}
	if deps.ContinuationScheduler == nil {
		return nil, fmt.Errorf("%w: continuation scheduler", ErrMissingDependency)
	}
	svc := &Engine{
		cfg:                   cfg,
		repo:                  deps.Store,
		threadContext:         deps.ThreadContext,
		projectionContent:     deps.ProjectionContent,
		turnProjections:       deps.TurnProjections,
		attachments:           deps.Attachments,
		settingsRepo:          deps.Settings,
		cache:                 deps.Cache,
		llmGateway:            deps.TextModelGateway,
		memoryRecorder:        deps.Memory,
		toolCatalog:           deps.ToolCatalog,
		toolExecutor:          deps.ToolExecutor,
		workspaces:            deps.Workspaces,
		embeddingSvc:          deps.Knowledge.Embedding,
		ragSvc:                deps.Knowledge.Retrieval,
		skillResolver:         deps.Skills,
		environmentProfiles:   deps.EnvironmentProfiles,
		unitOfWork:            deps.UnitOfWork,
		billingSvc:            deps.Billing,
		auditWriter:           deps.Audit,
		logger:                deps.Logger,
		tracer:                deps.Tracer,
		continuationScheduler: deps.ContinuationScheduler,
		generationStreams:     newGenerationStreamRegistry(deps.Cache, defaultGenerationStreamOptions()),
		runQueueWake:          make(chan struct{}, 1),
	}
	return svc, nil
}

// InvalidateMemoryCache 清除指定用户的记忆缓存，使下一次请求重新从 DB 加载。
// 由外部（memory handler 写入后）通过回调触发，避免循环依赖。
func (s *Engine) InvalidateMemoryCache(actor domain.ActorRef) {
	s.userMemCache.Delete(actor.TenantID + ":" + actor.ActorID)
}

// DeleteEventLogsBefore applies retention to Conversation-owned run events.
func (s *Engine) DeleteEventLogsBefore(ctx context.Context, before time.Time) (int64, error) {
	return s.repo.DeleteRunEventsBefore(ctx, before)
}
