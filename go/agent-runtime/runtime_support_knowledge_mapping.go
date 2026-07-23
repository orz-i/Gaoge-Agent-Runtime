// Package agentruntime owns Agent Runtime use cases and policy.
package agentruntime

import (
	domainconversation "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func conversationRAGChunksFromKnowledge(chunks []RetrievalChunk) []domainconversation.RAGChunk {
	result := make([]domainconversation.RAGChunk, 0, len(chunks))
	for _, chunk := range chunks {
		result = append(result, domainconversation.RAGChunk{
			Content: chunk.Content, FileName: chunk.FileName, FileID: chunk.FileID,
			ChunkIndex: chunk.ChunkIndex, Score: chunk.Score,
		})
	}
	return result
}
