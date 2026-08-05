package agentruntime

type Config struct {
	Attribution AttributionConfig
	Context     ContextConfig
	Files       FileContextConfig
	Retrieval   RetrievalConfig
	Execution   ExecutionConfig
	Planner     PlannerConfig
	Tools       ToolConfig
	Outputs     OutputConfig
	Retention   RetentionConfig
	Trace       TraceConfig
	Workflow    WorkflowConfig
}

// WorkflowConfig contains absolute engine ceilings. A Definition and a start
// request may narrow these values but can never raise them.
type WorkflowConfig struct {
	MaxNodeActivations int
	MaxChildRuns       int
	MaxConcurrentRuns  int
	MaxTotalLLMCalls   int
	MaxTotalToolCalls  int
	MaxDurationSeconds int
	MaxLoopIterations  int
	MaxNestedDepth     int
	MaxStateBytes      int
	MaxExpressionDepth int
	MaxExpressionOps   int
	MaxExpressionBytes int
	MaxDefinitionNodes int
	MaxCacheTTLSeconds int

	// Segment limits are cooperative execution bounds. They do not change the
	// workflow's aggregate budget; they force a durable checkpoint before one
	// continuation monopolizes a worker or builds an oversized transition.
	MaxSegmentNodeActivations int
	MaxSegmentDurationMS      int
	MaxSegmentEffects         int
	MaxSegmentTransitionBytes int
}

type AttributionConfig struct {
	AppName          string
	PublicWebBaseURL string
}

type ContextConfig struct {
	MaxMessages             int    `json:"maxMessages"`
	MaxTurns                int    `json:"maxTurns"`
	MaxInputTokens          int    `json:"maxInputTokens"`
	PreserveRecentTurns     int    `json:"preserveRecentTurns"`
	SoftLimitPercent        int    `json:"softLimitPercent"`
	SummaryMaxTokens        int    `json:"summaryMaxTokens"`
	EstimateSafetyPercent   int    `json:"estimateSafetyPercent"`
	ManagementMode          string `json:"managementMode"`
	TokenBudgetEnabled      bool   `json:"tokenBudgetEnabled"`
	MessageEmbeddingEnabled bool   `json:"messageEmbeddingEnabled"`
	SemanticEnabled         bool   `json:"semanticEnabled"`
}

const (
	ContextManagementManaged = "managed"
	ContextManagementLegacy  = "legacy"
)

func normalizeContextConfig(input ContextConfig) ContextConfig {
	if input.ManagementMode != ContextManagementLegacy {
		input.ManagementMode = ContextManagementManaged
	}
	if input.MaxTurns <= 0 {
		input.MaxTurns = 48
	}
	if input.MaxMessages <= 0 {
		input.MaxMessages = 20
	}
	if input.PreserveRecentTurns <= 0 {
		input.PreserveRecentTurns = 8
	}
	if input.SoftLimitPercent <= 0 || input.SoftLimitPercent >= 100 {
		input.SoftLimitPercent = 80
	}
	if input.SummaryMaxTokens <= 0 {
		input.SummaryMaxTokens = 1024
	}
	if input.EstimateSafetyPercent <= 0 {
		input.EstimateSafetyPercent = 15
	}
	return input
}

type FileContextConfig struct {
	MaxUploadBytes       int64
	MaxAttachments       int
	ImageMaxDimension    int
	FullContextMaxBytes  int64
	ImageMaxBytes        int64
	DocumentMaxBytes     int64
	FullContextMaxTokens int
	FullContextPDFPages  int
}

type RetrievalConfig struct {
	EmbeddingEnabled          bool
	EmbeddingOutputDimensions int
	EmbeddingNormalize        bool
	EmbeddingModelSignature   string
	Model                     string
	Enabled                   bool
	MinSimilarity             float64
	TokenBudget               int
	WaitReadyMS               int
	QueryHistoryTurns         int
}

type ModelOptionConfig struct {
	Mode         string
	AllowedPaths string
	DeniedPaths  string
}

type ExecutionConfig struct {
	DefaultSystemPrompt string
	SkillsPrompt        string
	ModelOptions        ModelOptionConfig
	MaxLLMCalls         int
	MaxToolCalls        int
	InteractionTTLHours int
}

type PlannerConfig struct {
	MaxSteps     int
	MaxRevisions int
}

type ToolConfig struct {
	Prompt             string
	RetryCount         int
	MaxConcurrentCalls int
	MaxSelectedPerRun  int
}

type OutputConfig struct {
	MaxPerRun int
}

type RetentionConfig struct {
	ContextArtifactDays int
}

type TraceConfig struct {
	Enabled            bool
	VisibleToUser      bool
	StoreUpstreamThink bool
	PersistInflight    bool
}

const (
	DefaultMCPMaxSelectedToolsPerMessage = 32
	MaxMCPSelectedToolsPerMessage        = 128
)

func DefaultModelOptionAllowedPathsJSON() string {
	return "{\"default\":[\"temperature\",\"top_p\",\"max_tokens\",\"max_output_tokens\",\"max_completion_tokens\",\"stop\",\"response_format.type\"]," +
		"\"openai_chat_completions\":[\"service_tier\",\"presence_penalty\",\"frequency_penalty\",\"reasoning_effort\",\"verbosity\",\"thinking.type\",\"stream_options.include_usage\"]," +
		"\"openrouter_chat_completions\":[\"presence_penalty\",\"frequency_penalty\",\"reasoning_effort\",\"reasoning.effort\",\"reasoning.summary\",\"verbosity\",\"thinking.type\",\"stream_options.include_usage\"]," +
		"\"openai_responses\":[\"service_tier\",\"reasoning.effort\",\"reasoning.summary\",\"text.verbosity\"]," +
		"\"openrouter_responses\":[\"reasoning.effort\",\"reasoning.summary\"]," +
		"\"anthropic_messages\":[\"speed\",\"top_k\",\"thinking.type\",\"thinking.budget_tokens\"]," +
		"\"xai_responses\":[\"reasoning.effort\"]," +
		"\"volcengine_responses\":[\"thinking.type\",\"store\"]," +
		"\"volcengine_video_generation\":[\"callback_url\",\"camerafixed\",\"draft\",\"duration\",\"execution_expires_after\",\"fps\",\"frames\",\"generate_audio\",\"image_role\",\"negative_prompt\",\"poll_interval_ms\",\"poll_timeout_ms\",\"ratio\",\"resolution\",\"return_last_frame\",\"seed\",\"service_tier\",\"watermark\"]," +
		"\"gemini_generate_content\":[\"generationConfig.temperature\",\"generationConfig.topP\",\"generationConfig.maxOutputTokens\",\"generationConfig.responseMimeType\"]}"
}

func DefaultModelOptionDeniedPathsJSON() string {
	return "{\"default\":[\"model\",\"messages\",\"input\",\"instructions\",\"prompt\",\"system\",\"systemInstruction\",\"headers\",\"api_key\",\"apiKey\",\"base_url\",\"baseURL\",\"stream\",\"previous_response_id\"]}"
}
