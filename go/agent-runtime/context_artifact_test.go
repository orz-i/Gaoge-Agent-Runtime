// Package conversation owns conversation use cases and policy.
package agentruntime

import (
	"encoding/json"
	"strings"
	"testing"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	valueAMd3F472AE1           = "A.md"
	valueBMd2BAE7069           = "B.md"
	valueAssistantEB7A6831     = "assistant"
	valueEvidence1CD33E62      = "evidence"
	valueFileA6227218F         = "file_a"
	valueFileB0FF1B87D         = "file_b"
	valueFunction8B7C099D      = "function"
	valueReady1BD6A4B1         = "ready"
	valueRetained9C816380      = "retained"
	valueRun14AAE66FD          = "run_1"
	valueSuccess550F5EAC       = "success"
	valueSummary65A530B0       = "summary"
	valueTokenCap85C62630      = "token_cap"
	valueActor11               = "actor_11"
	valueMessage8              = "message_8"
	valueMessage9              = "message_9"
	valueMessage13             = "message_13"
	valueTool64DAB6C8          = "tool"
	valueWebSearch43035A90     = "web_search"
	valueWebSearchCall762B62AF = "web_search_call"
)

func TestBuildPromptContextArtifactsRecordsRAGFallbackAndRecall(t *testing.T) {
	items := buildPromptContextArtifacts(promptContextArtifactInput{
		Actor:      model.ActorRef{TenantID: valueTenantTest, ActorID: valueActor11},
		Thread:     model.ThreadRef{Kind: threadKindConversation, ID: valueThread7},
		Projection: model.ProjectionRef{Kind: valueMessage5959AD4D, ID: valueMessage13},
		RunID:      valueRun14AAE66FD,
		Query:      "解释文件",
		RAGChunks: []model.RAGChunk{{
			FileID:     valueFileA6227218F,
			FileName:   valueAMd3F472AE1,
			ChunkIndex: 2,
			Content:    "RAG 命中的证据",
			Score:      0.87,
		}},
		RAGFallbacks: []ragFallbackEvidence{{
			Reason: "rag_empty",
			Attachment: AttachmentInput{
				FileID:        valueFileB0FF1B87D,
				FileName:      valueBMd2BAE7069,
				SHA256:        "sha_b",
				ExtractStatus: valueReady1BD6A4B1,
				EmbedStatus:   valueReady1BD6A4B1,
				ExtractedText: "全文回退证据",
			},
		}},
		RecallChunks: []model.RecallChunk{{
			Projection: model.ProjectionRef{Kind: valueMessage5959AD4D, ID: "message_3"},
			Role:       valueAssistantEB7A6831,
			ChunkIndex: 1,
			Content:    "历史语义召回证据",
			Similarity: 0.82,
		}},
		Memories: []MemoryItem{{
			MemoryKey: "language",
			Value:     "优先使用中文回答",
			Scope:     "profile",
			UpdatedBy: "manual",
		}},
	})

	if len(items) != 4 {
		t.Fatalf("expected 4 artifacts, got %#v", items)
	}
	if items[0].Kind != model.ContextArtifactFileRAGChunk || items[0].SourceID != "file_a:2" {
		t.Fatalf("expected file rag artifact, got %#v", items[0])
	}
	if items[1].Kind != model.ContextArtifactFileRAGFallback || items[1].SourceID != valueFileB0FF1B87D {
		t.Fatalf("expected fallback artifact, got %#v", items[1])
	}
	if !hasContextArtifact(items, model.ContextArtifactSemanticRecall, "message_3:1") {
		t.Fatalf("expected recall artifact, got %#v", items)
	}
	if !hasContextArtifact(items, model.ContextArtifactUserMemory, "language") {
		t.Fatalf("expected memory artifact, got %#v", items)
	}
	for _, item := range items {
		assertContextArtifactTraceFields(t, item)
	}
}

func assertContextArtifactTraceFields(t *testing.T, item model.ContextArtifact) {
	t.Helper()
	if item.Projection != (model.ProjectionRef{Kind: valueMessage5959AD4D, ID: valueMessage13}) || item.RunID != valueRun14AAE66FD {
		t.Fatalf("artifact identity mismatch: %#v", item)
	}
	if item.ContentHash == "" || item.TokenEstimate <= 0 || item.MetadataJSON == "" {
		t.Fatalf("artifact missing trace fields: %#v", item)
	}
	var metadata map[string]interface{}
	if err := json.Unmarshal([]byte(item.MetadataJSON), &metadata); err != nil {
		t.Fatalf("invalid metadata json: %v", err)
	}
}

func hasContextArtifact(items []model.ContextArtifact, kind model.ContextArtifactKind, sourceID string) bool {
	for _, item := range items {
		if item.Kind == kind && item.SourceID == sourceID {
			return true
		}
	}
	return false
}

func TestBuildPromptContextArtifactsSkipsEmptyEvidence(t *testing.T) {
	items := buildPromptContextArtifacts(promptContextArtifactInput{
		RAGChunks: []model.RAGChunk{{
			FileID:     valueFileA6227218F,
			ChunkIndex: 1,
			Content:    " ",
		}},
		RAGFallbacks: []ragFallbackEvidence{{
			Attachment: AttachmentInput{
				FileID:        valueFileB0FF1B87D,
				ExtractedText: "",
			},
		}},
		RecallChunks: []model.RecallChunk{{
			Projection: model.ProjectionRef{Kind: valueMessage5959AD4D, ID: "message_1"},
			Content:    "",
		}},
		Memories: []MemoryItem{{
			MemoryKey: "empty",
			Value:     "",
		}},
	})
	if len(items) != 0 {
		t.Fatalf("expected empty artifacts, got %#v", items)
	}
}

func TestApplyContextArtifactRetentionSetsExpiresAt(t *testing.T) {
	svc := &Engine{cfg: StaticConfigProvider(Config{Retention: RetentionConfig{ContextArtifactDays: 7}})}
	items := []model.ContextArtifact{{Content: valueEvidence1CD33E62}}

	svc.applyContextArtifactRetention(items)

	if items[0].ExpiresAt == nil {
		t.Fatal("expected expires_at to be set")
	}
	if !items[0].ExpiresAt.After(items[0].CreatedAt) {
		t.Fatalf("expected future expires_at, got %#v", items[0].ExpiresAt)
	}
}

func TestApplyContextArtifactRetentionCanBeDisabled(t *testing.T) {
	svc := &Engine{cfg: StaticConfigProvider(Config{})}
	items := []model.ContextArtifact{{Content: valueEvidence1CD33E62}}

	svc.applyContextArtifactRetention(items)

	if items[0].ExpiresAt != nil {
		t.Fatalf("expected no expires_at, got %#v", items[0].ExpiresAt)
	}
}

func TestBuildToolContextArtifactsRecordsLocalAndNativeTools(t *testing.T) {
	items := buildToolContextArtifacts(toolContextArtifactInput{
		Actor:      model.ActorRef{TenantID: valueTenantTest, ActorID: valueActor11},
		Thread:     model.ThreadRef{Kind: threadKindConversation, ID: valueThread7},
		Projection: model.ProjectionRef{Kind: valueMessage5959AD4D, ID: valueMessage13},
		RunID:      valueRun14AAE66FD,
		Rows: []model.ToolRecord{
			{
				ToolCallID: "call_local",
				ToolType:   valueFunction8B7C099D,
				ToolName:   "search_web",
				Status:     valueSuccess550F5EAC,
				InputJSON:  `{"query":"Gaoge"}`,
				OutputJSON: `{"answer":"result"}`,
			},
			{
				ToolCallID: "call_native",
				ToolType:   valueWebSearchCall762B62AF,
				ToolName:   valueWebSearch43035A90,
				Status:     valueSuccess550F5EAC,
				OutputJSON: `{"url":"https://example.com"}`,
			},
		},
	})
	if len(items) != 2 {
		t.Fatalf("expected two tool artifacts, got %#v", items)
	}
	if items[0].Kind != model.ContextArtifactToolResult || items[0].SourceID != "call_local" {
		t.Fatalf("expected local tool artifact, got %#v", items[0])
	}
	if items[1].Kind != model.ContextArtifactNativeTool || items[1].SourceID != "call_native" {
		t.Fatalf("expected native tool artifact, got %#v", items[1])
	}
}

func TestBuildToolContextArtifactsKeepsHeadAndTailForLargeResults(t *testing.T) {
	items := buildToolContextArtifacts(toolContextArtifactInput{
		Actor:      model.ActorRef{TenantID: valueTenantTest, ActorID: valueActor11},
		Thread:     model.ThreadRef{Kind: threadKindConversation, ID: valueThread7},
		Projection: model.ProjectionRef{Kind: valueMessage5959AD4D, ID: valueMessage13},
		RunID:      valueRun14AAE66FD,
		Rows: []model.ToolRecord{{
			ToolCallID: "call_large",
			ToolType:   valueFunction8B7C099D,
			ToolName:   "fetch_large",
			Status:     valueSuccess550F5EAC,
			OutputJSON: "HEAD-" + strings.Repeat("x", contextArtifactExcerptChars+256) + "-TAIL",
		}},
	})
	if len(items) != 1 {
		t.Fatalf("expected one tool artifact, got %#v", items)
	}
	if !strings.Contains(items[0].Content, "HEAD-") || !strings.Contains(items[0].Content, "-TAIL") {
		t.Fatalf("expected large artifact content to preserve head and tail, got %q", items[0].Content)
	}
	if !strings.Contains(items[0].MetadataJSON, `"truncated":true`) {
		t.Fatalf("expected truncated metadata, got %q", items[0].MetadataJSON)
	}
}

func TestBuildSnapshotContextArtifactRecordsSummary(t *testing.T) {
	item := buildSnapshotContextArtifact(snapshotContextArtifactInput{
		Actor:      model.ActorRef{TenantID: valueTenantTest, ActorID: valueActor11},
		Thread:     model.ThreadRef{Kind: threadKindConversation, ID: valueThread7},
		Projection: model.ProjectionRef{Kind: valueMessage5959AD4D, ID: "message_14"},
		RunID:      valueRun14AAE66FD,
		Snapshot: &ThreadCompaction{
			CoveredThrough: model.ProjectionRef{Kind: valueMessage5959AD4D, ID: "3"},
			FromTurn:       1,
			ToTurn:         6,
			SourceTokens:   1000,
			SummaryTokens:  120,
			Summary:        "压缩摘要内容",
			Strategy:       valueTokenCap85C62630,
		},
	})
	if item == nil {
		t.Fatal("expected snapshot artifact")
	}
	if item.Kind != model.ContextArtifactSummary || item.SourceID != "3" || item.Projection.ID != "message_14" {
		t.Fatalf("unexpected snapshot artifact: %#v", item)
	}
	if item.TokenEstimate != 120 || item.ContentHash == "" || item.MetadataJSON == "" {
		t.Fatalf("snapshot artifact missing fields: %#v", item)
	}
}

func TestSelectHistoricalContextArtifactsUsesFollowUpAndDeduplicatesCurrentEvidence(t *testing.T) {
	items := selectHistoricalContextArtifacts(historicalContextArtifactInput{
		CurrentProjection: valueMessage9,
		Query:             "把刚才这个文件总结短一点",
		CurrentRAGChunks: []model.RAGChunk{{
			Content: "当前轮已经命中的重复证据",
		}},
		Candidates: []model.ContextArtifact{
			{
				Projection:    model.ProjectionRef{Kind: valueMessage5959AD4D, ID: valueMessage8},
				Kind:          model.ContextArtifactFileRAGChunk,
				SourceTitle:   valueAMd3F472AE1,
				Content:       "当前轮已经命中的重复证据",
				TokenEstimate: 10,
			},
			{
				Projection:    model.ProjectionRef{Kind: valueMessage5959AD4D, ID: "message_7"},
				Kind:          model.ContextArtifactFileRAGChunk,
				SourceTitle:   valueBMd2BAE7069,
				Content:       "旧轮文件证据，说明系统分层和测试要求。",
				TokenEstimate: 10,
			},
			{
				Projection:    model.ProjectionRef{Kind: valueMessage5959AD4D, ID: valueMessage9},
				Kind:          model.ContextArtifactFileRAGChunk,
				SourceTitle:   "current.md",
				Content:       "当前消息自己的证据不应被召回。",
				TokenEstimate: 10,
			},
		},
	})

	if len(items) != 1 {
		t.Fatalf("expected one historical artifact, got %#v", items)
	}
	if items[0].SourceTitle != valueBMd2BAE7069 {
		t.Fatalf("expected B.md artifact, got %#v", items[0])
	}
}

func TestSelectHistoricalContextArtifactsSkipsSummaryWhenCurrentSnapshotExists(t *testing.T) {
	items := selectHistoricalContextArtifacts(historicalContextArtifactInput{
		CurrentProjection:  valueMessage9,
		HasCurrentSnapshot: true,
		Query:              "继续总结刚才的内容",
		Candidates: []model.ContextArtifact{
			{
				Projection:    model.ProjectionRef{Kind: valueMessage5959AD4D, ID: "message_7"},
				Kind:          model.ContextArtifactSummary,
				SourceTitle:   valueSummary65A530B0,
				Content:       "旧摘要内容",
				TokenEstimate: 10,
				Score:         1,
			},
			{
				Projection:    model.ProjectionRef{Kind: valueMessage5959AD4D, ID: valueMessage8},
				Kind:          model.ContextArtifactToolResult,
				SourceTitle:   valueTool64DAB6C8,
				Content:       "工具返回的部署结果",
				TokenEstimate: 10,
				Score:         1,
			},
		},
	})

	if len(items) != 1 {
		t.Fatalf("expected one non-summary artifact, got %#v", items)
	}
	if items[0].Kind != model.ContextArtifactToolResult {
		t.Fatalf("expected tool artifact to remain, got %#v", items[0])
	}
}

func TestSelectHistoricalContextArtifactsRespectsSnapshotScope(t *testing.T) {
	items := selectHistoricalContextArtifacts(historicalContextArtifactInput{
		CurrentProjection:  valueMessage9,
		HasCurrentSnapshot: true,
		CoveredThrough:     "message_4",
		AllowedProjections: map[string]struct{}{
			"message_6": {},
		},
		Query: "继续部署测试",
		Candidates: []model.ContextArtifact{
			{
				Projection:    model.ProjectionRef{Kind: valueMessage5959AD4D, ID: "message_4"},
				Kind:          model.ContextArtifactToolResult,
				SourceTitle:   "covered",
				Content:       "已被摘要覆盖的部署测试结果",
				TokenEstimate: 10,
				Score:         1,
			},
			{
				Projection:    model.ProjectionRef{Kind: valueMessage5959AD4D, ID: "message_6"},
				Kind:          model.ContextArtifactToolResult,
				SourceTitle:   valueRetained9C816380,
				Content:       "保留窗口内的部署测试结果",
				TokenEstimate: 10,
				Score:         1,
			},
			{
				Projection:    model.ProjectionRef{Kind: valueMessage5959AD4D, ID: valueMessage8},
				Kind:          model.ContextArtifactToolResult,
				SourceTitle:   "sibling",
				Content:       "其他分支的部署测试结果",
				TokenEstimate: 10,
				Score:         1,
			},
			{
				Projection:    model.ProjectionRef{},
				Kind:          model.ContextArtifactToolResult,
				SourceTitle:   "unanchored",
				Content:       "没有消息锚点的部署测试结果",
				TokenEstimate: 10,
				Score:         1,
			},
		},
	})

	if len(items) != 1 {
		t.Fatalf("expected one retained-scope artifact, got %#v", items)
	}
	if items[0].SourceTitle != valueRetained9C816380 {
		t.Fatalf("expected retained artifact, got %#v", items[0])
	}
}

func TestSelectHistoricalContextArtifactsRequiresRelevanceWithoutFollowUp(t *testing.T) {
	items := selectHistoricalContextArtifacts(historicalContextArtifactInput{
		Query: "部署 测试",
		Candidates: []model.ContextArtifact{
			{
				Projection:    model.ProjectionRef{Kind: valueMessage5959AD4D, ID: "message_1"},
				Kind:          model.ContextArtifactFileRAGChunk,
				SourceTitle:   "ops.md",
				Content:       "上线部署前必须先跑测试。",
				TokenEstimate: 10,
			},
			{
				Projection:    model.ProjectionRef{Kind: valueMessage5959AD4D, ID: "message_2"},
				Kind:          model.ContextArtifactFileRAGChunk,
				SourceTitle:   "music.md",
				Content:       "歌单统计结果。",
				TokenEstimate: 10,
			},
		},
	})

	if len(items) != 1 {
		t.Fatalf("expected one relevant artifact, got %#v", items)
	}
	if items[0].SourceTitle != "ops.md" {
		t.Fatalf("expected ops.md artifact, got %#v", items[0])
	}
}
