// Package agentruntime owns Agent Runtime use cases and policy.
package agentruntime

import (
	"context"
	"io"
	"time"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

// FileAsset is Conversation's hydrated consumer projection of a Knowledge file.
// It is intentionally owned here and never escapes as a Knowledge domain type.
type FileAsset struct {
	FileID                 string
	Purpose                string
	FileName               string
	MimeType               string
	DetectedMIME           string
	FileCategory           string
	SizeBytes              int64
	SHA256                 string
	Status                 string
	LastAccessedAt         *time.Time
	ExpiresAt              *time.Time
	ProcessingStatus       string
	ProcessingReady        bool
	ProcessingErrorCode    string
	ProcessingErrorMessage string
	ExtractStatus          string
	EmbedStatus            string
	EmbedError             string
	PageCount              int
	ChunkCount             int
	RagOptOut              bool
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

type FileContentResult struct {
	FileName, ContentType string
	Reader                io.ReadCloser
	SizeBytes             int64
}

type RetrievalStatus string

const (
	RetrievalStatusHit      RetrievalStatus = "hit"
	RetrievalStatusLowScore RetrievalStatus = "low_score"
	RetrievalStatusEmpty    RetrievalStatus = "empty"
)

type RetrievalChunk struct {
	Content    string
	FileName   string
	FileID     string
	ChunkIndex int
	Score      float32
}

type RetrievalResult struct {
	Chunks         []RetrievalChunk
	Status         RetrievalStatus
	Reason         string
	CandidateCount int
	FilteredCount  int
	MaxScore       float32
	Cached         bool
}

type EmbeddingPort interface {
	EmbedTexts(context.Context, []string) ([][]float32, error)
}

type RetrievalPort interface {
	FileIndexAvailable(context.Context) (bool, string)
	Retrieve(context.Context, domain.ActorRef, string, []FileAsset) (RetrievalResult, error)
}

type KnowledgeDependencies struct {
	Embedding EmbeddingPort
	Retrieval RetrievalPort
}
