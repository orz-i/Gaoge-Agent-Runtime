package domain

import "time"

type ContextArtifactKind string

const (
	ContextArtifactFileRAGChunk    ContextArtifactKind = "file_rag_chunk"
	ContextArtifactFileRAGFallback ContextArtifactKind = "file_rag_fallback"
	ContextArtifactSemanticRecall  ContextArtifactKind = "semantic_recall"
	ContextArtifactUserMemory      ContextArtifactKind = "user_memory"
	ContextArtifactToolResult      ContextArtifactKind = "tool_result"
	ContextArtifactNativeTool      ContextArtifactKind = "native_tool_result"
	ContextArtifactSummary         ContextArtifactKind = "thread_summary"
)

type ContextSnapshot struct {
	SnapshotID, RunID, ThreadPathHash, ContentJSON, ContentHash string
	SupersedesSnapshotID, ManagementStatus                      string
	SchemaVersion                                               int
	Revision                                                    int
	Actor                                                       ActorRef
	Thread                                                      ThreadRef
	InputProjection                                             ProjectionRef
	TokenEstimate                                               int64
	FileCount, RAGCount, SkillCount, MemoryCount                int
	OutputCount, EvidenceCount, RetrievalFallbackCount          int
	SkippedCount                                                int
	CreatedAt, UpdatedAt                                        time.Time
}

const (
	ContextManagementStatusBaseline = "baseline"
	ContextManagementStatusManaged  = "managed"
)

type ContextArtifact struct {
	ArtifactID, SnapshotID, RunID              string
	Kind                                       ContextArtifactKind
	Resource                                   ResourceRef
	Projection                                 ProjectionRef
	SourceType, SourceID, SourceTitle, Content string
	ContentJSON, ContentHash, MetadataJSON     string
	TokenEstimate                              int64
	Score                                      float64
	ExpiresAt                                  *time.Time
	CreatedAt, UpdatedAt                       time.Time
}

type RAGChunk struct {
	Content, FileName, FileID string
	ChunkIndex                int
	Score                     float32
}

type RecallChunk struct {
	Projection ProjectionRef
	Role       string
	ChunkIndex int
	Content    string
	TokenCount int
	Similarity float64
}

type ToolRecord struct {
	RunID, ToolCallID, ToolType, ToolName, Status string
	LatencyMS                                     int64
	InputJSON, OutputJSON, ErrorJSON              string
}
