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
}

type AttributionConfig struct {
	AppName          string
	PublicWebBaseURL string
}

type ContextConfig struct {
	MaxMessages             int
	MaxInputTokens          int
	CompactEnabled          bool
	CompactTrigger          int
	CompactPreserve         int
	TokenBudgetEnabled      bool
	MessageEmbeddingEnabled bool
	SemanticEnabled         bool
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
		"\"gemini_generate_content\":[\"generationConfig.temperature\",\"generationConfig.topP\",\"generationConfig.maxOutputTokens\",\"generationConfig.responseMimeType\"]}"
}

func DefaultModelOptionDeniedPathsJSON() string {
	return "{\"default\":[\"model\",\"messages\",\"input\",\"instructions\",\"prompt\",\"system\",\"systemInstruction\",\"headers\",\"api_key\",\"apiKey\",\"base_url\",\"baseURL\",\"stream\",\"previous_response_id\"]}"
}
