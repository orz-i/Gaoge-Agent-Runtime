// Package agentruntime owns Agent Runtime use cases and policy.
package agentruntime

import (
	"context"
	"strings"

	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
	"github.com/orz-i/Gaoge/sdk/go/agent-runtime/modelcap"
)

const (
	valueAssistant04D0D6FD = "assistant"
	valueDefaultD11B78AC   = "default"
	valueEdit08C9C683      = "edit"
	valueRetry301B2C26     = "retry"
	valueSystemE58486F0    = "system"
	valueUser6FBA7C13      = "user"
)

type messageBranchState struct {
	Path       ThreadPath
	Parent     *domain.ProjectionRef
	Source     *domain.ProjectionRef
	ReuseInput *domain.ProjectionRef
}

func (s *Engine) resolveMessageBranch(
	ctx context.Context,
	actor domain.ActorRef,
	thread domain.ThreadRef,
	parent *domain.ProjectionRef,
	source *domain.ProjectionRef,
	branchReason string,
) (*messageBranchState, error) {
	if s.threadContext == nil {
		return nil, ErrHostProjectionUnavailable
	}
	branchReason = firstNonEmptyString(strings.TrimSpace(branchReason), valueDefaultD11B78AC)
	if branchReason != valueDefaultD11B78AC && branchReason != valueRetry301B2C26 && branchReason != valueEdit08C9C683 {
		return nil, ErrInvalidThreadBranch
	}
	path, err := s.threadContext.LoadThreadPath(ctx, LoadThreadPathRequest{
		Actor: actor, Thread: thread, Head: parent, Source: source,
		BranchReason: branchReason, MaxDepth: s.contextMessageLimit(),
	})
	if err != nil {
		return nil, ErrInvalidThreadBranch
	}
	return &messageBranchState{Path: path, Parent: path.Parent, Source: path.Source, ReuseInput: path.ReuseInput}, nil
}

func (s *Engine) contextMessageLimit() int {
	if s == nil || s.cfg == nil {
		return 100
	}
	if limit := s.cfg.Snapshot().Context.MaxMessages; limit > 0 {
		return limit
	}
	return 100
}

func buildBranchMessagePath(branch *messageBranchState, userMessage *ContextMessage) []ContextMessage {
	if branch == nil || userMessage == nil {
		return nil
	}
	items := append([]ContextMessage(nil), branch.Path.Messages...)
	if branch.ReuseInput != nil {
		for index := range items {
			if items[index].Projection == *branch.ReuseInput {
				return items[:index+1]
			}
		}
	}
	return append(items, *userMessage)
}

func (s *Engine) applyContextTokenBudget(messages []ContextMessage, capabilityModelName, capabilitiesJSON string) []ContextMessage {
	if s == nil || s.cfg == nil || !s.cfg.Snapshot().Context.TokenBudgetEnabled || len(messages) <= 1 {
		return messages
	}
	return truncateContextByTokenBudget(messages, modelcap.Default.Resolve(capabilityModelName, capabilitiesJSON).EffectiveContextBudget())
}

func truncateContextByTokenBudget(messages []ContextMessage, budgetTokens int) []ContextMessage {
	if budgetTokens <= 0 || len(messages) == 0 {
		return messages
	}
	total, cutFrom := 0, len(messages)
	for index := len(messages) - 1; index >= 0; index-- {
		tokens := int(estimateTokens(messages[index].Content))
		if total+tokens > budgetTokens && cutFrom < len(messages) {
			break
		}
		total += tokens
		cutFrom = index
	}
	return messages[cutFrom:]
}

func buildRAGQuery(contextMessages []ContextMessage, currentContent string, historyTurns int) string {
	current := strings.TrimSpace(currentContent)
	if historyTurns <= 0 || len(contextMessages) == 0 {
		return current
	}
	recent := make([]string, 0, historyTurns)
	for index := len(contextMessages) - 2; index >= 0 && len(recent) < historyTurns; index-- {
		if contextMessages[index].Role != valueUser6FBA7C13 {
			continue
		}
		if snippet := compactSnippet(contextMessages[index].Content, 240); snippet != "" {
			recent = append(recent, snippet)
		}
	}
	if len(recent) == 0 {
		return current
	}
	var builder strings.Builder
	builder.WriteString(current)
	builder.WriteString("\n\nRecent user context:\n")
	for index := len(recent) - 1; index >= 0; index-- {
		builder.WriteString("- ")
		builder.WriteString(recent[index])
		builder.WriteByte('\n')
	}
	return strings.TrimSpace(builder.String())
}
