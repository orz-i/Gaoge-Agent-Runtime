package agentruntime

import (
	"bytes"
	"context"
	"io"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

func (s *Engine) openPromptFileContent(ctx context.Context, actor domain.ActorRef, fileID string) (io.ReadCloser, error) {
	if s.attachments == nil {
		return nil, ErrAttachmentNotFound
	}
	content, err := s.attachments.OpenAttachment(ctx, OpenAttachmentRequest{Actor: actor, Ref: domain.ResourceRef{Kind: valueFileBE372696, ID: fileID}})
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(content.Data)), nil
}
