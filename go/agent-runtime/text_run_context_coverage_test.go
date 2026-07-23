package agentruntime

import (
	"testing"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func TestTextRunContextFileCoverageSeparatesIncludedAndSkippedFiles(t *testing.T) {
	files := []textRunContextFileRef{
		{FileID: "direct", ContextMode: fileContextModeFull},
		{FileID: "retrieved", ContextMode: fileContextModeRAG},
		{FileID: "missed", ContextMode: fileContextModeRAG},
		{FileID: "too-large", ContextMode: fileContextModeSkipped},
	}
	artifacts := []model.ContextArtifact{{
		Kind:         model.ContextArtifactFileRAGChunk,
		MetadataJSON: `{"file_id":"retrieved"}`,
	}}

	included, skipped := textRunContextFileCoverage(files, artifacts)
	if included != 2 || skipped != 2 {
		t.Fatalf("coverage included=%d skipped=%d, want 2 and 2", included, skipped)
	}
}
