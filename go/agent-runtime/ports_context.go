package agentruntime

import (
	"context"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

// ThreadContextSource supplies a provider-neutral thread snapshot and message path.
type ThreadContextSource interface {
	ResolveThread(context.Context, ResolveThreadRequest) (ThreadSnapshot, error)
	LoadThreadPath(context.Context, LoadThreadPathRequest) (ThreadPath, error)
}

// ProjectionContentSource resolves immutable content snapshots from a host-owned projection.
type ProjectionContentSource interface {
	ResolveProjectionContent(context.Context, ResolveProjectionContentRequest) (ProjectionContent, error)
}

// TurnProjectionWriter projects a Runtime turn into its host thread.
type TurnProjectionWriter interface {
	BeginTurn(context.Context, BeginTurnRequest) (TurnProjection, error)
	CompleteTurn(context.Context, CompleteTurnRequest) (ProjectionWriteResult, error)
	FailTurn(context.Context, FailTurnRequest) (ProjectionWriteResult, error)
	CancelTurn(context.Context, CancelTurnRequest) (ProjectionWriteResult, error)
}

// AttachmentResolver resolves provider-neutral resource references for a turn.
type AttachmentResolver interface {
	ResolveAttachments(context.Context, ResolveAttachmentsRequest) (ResolveAttachmentsResult, error)
	OpenAttachment(context.Context, OpenAttachmentRequest) (AttachmentContent, error)
}

// UnitOfWork owns the transaction boundary for coordinated Runtime and host writes.
type UnitOfWork interface {
	Within(context.Context, func(context.Context) error) error
}

type ResolveThreadRequest struct {
	Actor  domain.ActorRef
	Thread domain.ThreadRef
}

type ResolveProjectionContentRequest struct {
	Actor      domain.ActorRef
	Thread     domain.ThreadRef
	Projection domain.ProjectionRef
}

type ProjectionContent struct {
	Title       string
	ContentType string
	Content     string
	ContentHash string
}

type ThreadSnapshot struct {
	Thread             domain.ThreadRef
	Title              string
	DefaultModel       string
	ModelProvider      string
	Environment        domain.ResourceRef
	BindingScope       string
	Compacted          bool
	Instructions       []ThreadInstruction
	ResourceRefs       []domain.ResourceRef
	ProviderResponseID string
	PromptFingerprint  string
}

type ThreadInstruction struct {
	Kind    string
	Content string
}

type LoadThreadPathRequest struct {
	Actor        domain.ActorRef
	Thread       domain.ThreadRef
	Head         *domain.ProjectionRef
	Source       *domain.ProjectionRef
	BranchReason string
	MaxDepth     int
}

type ThreadPath struct {
	Messages   []ContextMessage
	Parent     *domain.ProjectionRef
	Source     *domain.ProjectionRef
	ReuseInput *domain.ProjectionRef
	Compaction *ThreadCompaction
}

type ContextMessage struct {
	Projection  domain.ProjectionRef
	Parent      *domain.ProjectionRef
	Source      *domain.ProjectionRef
	RunID       string
	Role        string
	ContentType string
	Content     string
	Status      string
	Attachments []domain.ResourceRef
	CreatedAt   time.Time
}

type ThreadCompaction struct {
	CoveredThrough domain.ProjectionRef
	PathHash       string
	FromTurn       int
	ToTurn         int
	SourceTokens   int64
	SummaryTokens  int64
	Summary        string
	Strategy       string
}

type ResolveAttachmentsRequest struct {
	Actor      domain.ActorRef
	References []domain.ResourceRef
}

type ResolveAttachmentsResult struct {
	Attachments []ResolvedAttachment
}

type ResolvedAttachment struct {
	Ref                    domain.ResourceRef
	Kind                   string
	Name                   string
	MediaType              string
	DetectedMediaType      string
	Category               string
	SizeBytes              int64
	SHA256                 string
	PageCount              int
	ChunkCount             int
	ProcessingStatus       string
	ProcessingReady        bool
	ProcessingErrorCode    string
	ProcessingErrorMessage string
	ExtractStatus          string
	EmbedStatus            string
	RAGDisabled            bool
}

type OpenAttachmentRequest struct {
	Actor domain.ActorRef
	Ref   domain.ResourceRef
}

type AttachmentContent struct {
	Data      []byte
	MediaType string
	SHA256    string
}

type BeginTurnRequest struct {
	Actor         domain.ActorRef
	Thread        domain.ThreadRef
	RunID         string
	ContentType   string
	Content       string
	TokenEstimate int64
	Parent        *domain.ProjectionRef
	Source        *domain.ProjectionRef
	BranchReason  string
	Attachments   []ResolvedAttachment
}

type TurnProjection struct {
	Input  domain.ProjectionRef
	Output domain.ProjectionRef
	Parent *domain.ProjectionRef
	Source *domain.ProjectionRef
}

type TurnUsage struct {
	InputTokens      int64
	OutputTokens     int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	ReasoningTokens  int64
	LatencyMS        int64
	BilledCurrency   string
	BilledNanousd    int64
	PricingSnapshot  string
}

type CompleteTurnRequest struct {
	Actor              domain.ActorRef
	Thread             domain.ThreadRef
	RunID              string
	Projection         TurnProjection
	ContentType        string
	Content            string
	Usage              TurnUsage
	ProviderResponseID string
	PromptFingerprint  string
}

type FailTurnRequest struct {
	Actor        domain.ActorRef
	Thread       domain.ThreadRef
	RunID        string
	Projection   TurnProjection
	ContentType  string
	Content      string
	Usage        TurnUsage
	ErrorCode    string
	ErrorMessage string
}

type CancelTurnRequest struct {
	Actor        domain.ActorRef
	Thread       domain.ThreadRef
	RunID        string
	Projection   TurnProjection
	ErrorCode    string
	ErrorMessage string
}

type ProjectionWriteResult struct {
	Projection TurnProjection
	Applied    bool
}
