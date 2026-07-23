package agentruntime

import "time"

type MessagePromptTraceSourceRef struct {
	SourceType, SourceID, Title string
	ArtifactID                  string
}

type MessagePromptTraceBlock struct {
	Kind, Title   string
	TokenEstimate int64
	Cacheable     bool
	SourceCount   int
	SourceRefs    []MessagePromptTraceSourceRef
}

type MessagePromptTrace struct {
	Mode, PromptFingerprint, StatefulDisabledReason string
	StatefulUsed                                    bool
	TotalTokenEstimate, SentTokenEstimate           int64
	FullMessageCount, SentMessageCount              int
	StatefulSavedMessages                           int
	StatefulSavedTokens                             int64
	Blocks                                          []MessagePromptTraceBlock
}

type MessageTraceBlock struct {
	Title, Summary, ContentMarkdown, Status, Stage string
	RoundID, ParentEventID, PayloadJSON            string
	UpdatedAt                                      time.Time
}

type MessageTraceEvent struct {
	EventID, EventType, Phase, Stage, RoundID, ParentEventID string
	Title, Summary, ContentMarkdown, Status, PayloadJSON     string
	Seq                                                      int
	StartedAt, UpdatedAt                                     time.Time
	EndedAt                                                  *time.Time
}

type MessageProcessTrace struct {
	Enabled       bool
	Status        string
	Process       *MessageTraceBlock
	Tools         *MessageTraceBlock
	UpstreamThink *MessageTraceBlock
	PromptTrace   *MessagePromptTrace
	Events        []MessageTraceEvent
}
