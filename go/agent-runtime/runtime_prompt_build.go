// Package agentruntime owns Agent Runtime use cases and policy.
package agentruntime

import (
	"context"
	"fmt"
	"strings"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

const (
	valueAutoCD31408C     = "auto"
	valueFull8613571D     = "full"
	valueKindAFE46F24     = "kind"
	valueStatefulCE750CD9 = "stateful"
	valueStatusEA3605E1   = "status"
	valueUserB8281568     = "user"
)

type runtimePromptPrefetchData struct {
	userMemories []MemoryItem
}

type PromptBuildInput struct {
	RunInput                        RuntimeInput
	Thread                          *ThreadSnapshot
	Route                           *LLMRoute
	BranchState                     *messageBranchState
	UserMessage                     *ContextMessage
	RunID                           string
	BranchReason                    string
	RunSpan                         Span
	Trace                           *messageTraceRecorder
	DeferContextArtifactPersistence bool
}

type PromptBuildResult struct {
	contextMessages              []ContextMessage
	promptPlan                   PromptPlan
	llmMessages                  []Message
	fullLLMMessages              []Message
	routeConfig                  RouteConfig
	generateInput                GenerateInput
	filteredOptions              map[string]interface{}
	toolRuntime                  selectedToolRuntime
	estimatedPromptTokens        int64
	stableFullContextAttachments []AttachmentInput
	allContextAttachments        []AttachmentInput
	prefixMemories               []MemoryItem
	statefulContextConfig        string
	statefulContextState         string
	statefulPrefixFingerprint    string
	statefulDecision             statefulResponseDecision
	initialPromptShape           promptShape
	contextArtifacts             []model.ContextArtifact
}

func (s *Engine) compileRuntimePrompt(ctx context.Context, input PromptBuildInput) (PromptBuildResult, error) {
	cfg := s.cfg.Snapshot()
	prefetchCh := s.startRunPromptPrefetch(ctx, input.RunInput)
	fileMode, capability := s.resolveRunFileMode(ctx, input.RunInput)

	prefetch := <-prefetchCh
	contextMessages := buildBranchMessagePath(input.BranchState, input.UserMessage)
	promptScope := buildPromptScope(contextMessages, input.BranchState.Path.Compaction)
	promptMessages := s.applyContextTokenBudget(promptScope.activeMessages(), input.Route.UpstreamModel, input.Route.ModelCapabilitiesJSON)
	ragQuery := buildRAGQuery(promptMessages, input.RunInput.Content, cfg.Retrieval.QueryHistoryTurns)

	fileContextPlan, err := s.prepareRunFileContext(ctx, input, cfg, promptMessages, fileMode, capability)
	if err != nil {
		return PromptBuildResult{}, err
	}
	base := s.buildRunBaseContext(ctx, input, cfg, promptScope, prefetch, promptMessages, fileMode, fileContextPlan)
	ragContext := s.resolveRunRAGContext(ctx, input.RunInput, cfg, fileContextPlan, ragQuery, base.assembler, input.Trace)
	stableAttachments := runtimeStableFullContextAttachments(fileContextPlan, ragContext.retrievalFallbacks)
	contextArtifacts := s.completeRunDynamicContext(ctx, input, promptScope, nil, ragQuery, ragContext, stableAttachments, &base.userCtx)

	skillPrompts, err := s.resolveRunSkillPrompts(ctx, input)
	if err != nil {
		return PromptBuildResult{}, err
	}
	result := s.buildRunPromptRequest(ctx, input, cfg, base, stableAttachments, skillPrompts)
	result.contextMessages = contextMessages
	result.contextArtifacts = contextArtifacts
	result.allContextAttachments = append([]AttachmentInput(nil), fileContextPlan.Attachments...)
	return result, nil
}

func (s *Engine) startRunPromptPrefetch(
	ctx context.Context,
	input RuntimeInput,
) <-chan runtimePromptPrefetchData {
	prefetchCh := make(chan runtimePromptPrefetchData, 1)
	go func() {
		var result runtimePromptPrefetchData
		if s.memoryRecorder != nil && input.MemoryEnabled {
			result.userMemories, _ = s.getCachedUserMemories(ctx, input.Actor)
		}
		prefetchCh <- result
	}()
	return prefetchCh
}

func (s *Engine) resolveRunFileMode(ctx context.Context, input RuntimeInput) (string, chatFileCapability) {
	fileMode := valueAutoCD31408C
	capability := s.resolveChatFileCapability(ctx)
	if value, err := s.getUserSettingCached(ctx, input.Actor, "chat.file_mode"); err == nil && value != "" {
		fileMode = value
	}
	return fileMode, capability
}

func (s *Engine) prepareRunFileContext(
	ctx context.Context,
	input PromptBuildInput,
	cfg Config,
	promptMessages []ContextMessage,
	fileMode string,
	capability chatFileCapability,
) (conversationFileContextPlan, error) {
	resourceFileIDs := make([]string, 0)
	if input.Thread != nil {
		for _, ref := range input.Thread.ResourceRefs {
			if ref.Kind == valueFileBE372696 {
				resourceFileIDs = append(resourceFileIDs, ref.ID)
			}
		}
	}
	threadFileIDs := collectConversationAndProjectFileIDs(promptMessages, resourceFileIDs, input.RunInput.FileIDs)
	conversationAttachments, err := s.resolveConversationFileContext(ctx, input.RunInput.Actor, threadFileIDs, input.RunInput.FileIDs)
	if err != nil {
		return conversationFileContextPlan{}, err
	}
	conversationAttachments, err = s.hydrateAttachmentsForRun(ctx, input.RunInput.Actor, conversationAttachments, input.RunInput.OnEvent)
	if err != nil {
		return conversationFileContextPlan{}, err
	}
	return buildConversationFileContextPlan(
		conversationAttachments,
		fileMode,
		cfg,
		input.Route.UpstreamModel,
		input.Route.ModelCapabilitiesJSON,
		capability.RAGAvailable,
	), nil
}

type runtimeBaseContextResult struct {
	llmMessages    []Message
	userCtx        userContextInput
	prefixMemories []MemoryItem
	assembler      *ContextAssembler
}

func (s *Engine) buildRunBaseContext(
	ctx context.Context,
	input PromptBuildInput,
	cfg Config,
	promptScope promptScope,
	prefetch runtimePromptPrefetchData,
	promptMessages []ContextMessage,
	fileMode string,
	fileContextPlan conversationFileContextPlan,
) runtimeBaseContextResult {
	historyMsgs := runtimeHistoryMessages(promptMessages, input.RunInput.Content)
	assembler := NewContextAssembler(int64(cfg.Context.MaxInputTokens))
	applyRunSystemPrompt(assembler, &historyMsgs, cfg, input)
	userCtx := runtimeSnapshotUserContext(promptScope)
	prefixMemories := s.applyRunMemoryContext(ctx, input, prefetch.userMemories, assembler, &userCtx)
	llmMessages, _ := assembler.Assemble(historyMsgs)
	if input.Trace != nil {
		summary, markdown, payload := buildAttachmentProcessTrace(fileMode, fileContextPlan.Attachments)
		input.Trace.appendProcessSection(summary, markdown, payload)
	}
	return runtimeBaseContextResult{
		llmMessages:    llmMessages,
		userCtx:        userCtx,
		prefixMemories: prefixMemories,
		assembler:      assembler,
	}
}

func runtimeHistoryMessages(promptMessages []ContextMessage, content string) []Message {
	historyMsgs := historyMessagesFromDomain(promptMessages)
	if len(historyMsgs) > 0 {
		return historyMsgs
	}
	return []Message{{Role: valueUserB8281568, Content: content}}
}

func applyRunSystemPrompt(
	assembler *ContextAssembler,
	historyMsgs *[]Message,
	cfg Config,
	input PromptBuildInput,
) {
	systemPrompt := resolveMessageSystemPromptInjection(
		cfg,
		input.Route,
		threadSystemPrompt(input.Thread),
		input.RunInput.HTMLVisualPromptEnabled,
		input.RunInput.HTMLVisualColorMode,
	)
	if profileInstructions := strings.TrimSpace(input.RunInput.Instructions); profileInstructions != "" {
		if strings.TrimSpace(systemPrompt.Content) == "" {
			systemPrompt.Content = profileInstructions
		} else {
			systemPrompt.Content += "\n\n<profile_instructions>\n" + profileInstructions + "\n</profile_instructions>"
		}
	}
	if systemPrompt.Content == "" {
		return
	}
	if systemPrompt.InlineToUser {
		*historyMsgs = inlineSystemPromptIntoLatestUserMessage(*historyMsgs, systemPrompt.Content)
		return
	}
	assembler.Add(ContextSlot{Kind: SlotSystemPrompt, Content: systemPrompt.Content, Required: true})
}

func threadSystemPrompt(thread *ThreadSnapshot) string {
	if thread == nil {
		return ""
	}
	parts := make([]string, 0, len(thread.Instructions))
	for _, instruction := range thread.Instructions {
		if strings.TrimSpace(instruction.Content) != "" {
			parts = append(parts, strings.TrimSpace(instruction.Content))
		}
	}
	return strings.Join(parts, "\n\n")
}

func runtimeSnapshotUserContext(promptScope promptScope) userContextInput {
	if promptScope.Compaction == nil {
		return userContextInput{}
	}
	summary := strings.TrimSpace(promptScope.Compaction.Summary)
	if summary == "" {
		return userContextInput{}
	}
	return userContextInput{
		Snapshot: &snapshotContext{
			Summary:  summary,
			FromTurn: promptScope.Compaction.FromTurn,
			ToTurn:   promptScope.Compaction.ToTurn,
			Strategy: promptScope.Compaction.Strategy,
		},
	}
}

func (s *Engine) applyRunMemoryContext(
	ctx context.Context,
	input PromptBuildInput,
	memories []MemoryItem,
	assembler *ContextAssembler,
	userCtx *userContextInput,
) []MemoryItem {
	if len(memories) == 0 {
		return nil
	}
	prefixMemories := filterMemoriesByScope(memories, "preference")
	if len(prefixMemories) > 0 {
		if prefContent := buildPreferencePrompt(prefixMemories, 400); prefContent != "" {
			assembler.Add(ContextSlot{Kind: SlotPreference, Content: prefContent})
		}
	}
	otherMems := filterMemoriesByScope(memories, "profile", "custom")
	if len(otherMems) > 0 {
		userCtx.Memory = s.selectRelevantUserMemories(ctx, input.RunInput.Actor, input.RunInput.Content, otherMems, 5)
	}
	return prefixMemories
}

func runtimeStableFullContextAttachments(fileContextPlan conversationFileContextPlan, retrievalFallbacks []ragFallbackEvidence) []AttachmentInput {
	stableAttachments := append([]AttachmentInput{}, fileContextPlan.FullAttachments...)
	return append(stableAttachments, ragFallbackEvidenceAttachments(retrievalFallbacks)...)
}

func (s *Engine) completeRunDynamicContext(
	ctx context.Context,
	input PromptBuildInput,
	promptScope promptScope,
	recallCh chan []model.RecallChunk,
	ragQuery string,
	ragContext runtimeRAGContextResult,
	stableAttachments []AttachmentInput,
	userCtx *userContextInput,
) []model.ContextArtifact {
	userCtx.Attachments = imageAttachmentsForCurrentUser(stableAttachments)
	userCtx.RAGChunks = ragContext.chunks
	if recallCh != nil {
		userCtx.RecallChunks = <-recallCh
	}
	coveredThrough := ""
	if promptScope.Compaction != nil {
		coveredThrough = promptScope.Compaction.CoveredThrough.ID
	}
	userCtx.HistoricalArtifacts = s.recallHistoricalContextArtifacts(
		ctx,
		input.RunInput.Actor,
		input.RunInput.Thread,
		input.UserMessage.Projection.ID,
		promptScope.Compaction != nil,
		coveredThrough,
		promptScope.retainedProjectionIDSet(),
		input.RunInput.Content,
		ragContext.chunks,
		ragFallbackEvidenceAttachments(ragContext.fallbacks),
		userCtx.RecallChunks,
	)
	artifacts := buildPromptContextArtifacts(promptContextArtifactInput{
		Actor:        input.RunInput.Actor,
		Thread:       input.RunInput.Thread,
		Projection:   input.UserMessage.Projection,
		RunID:        input.RunID,
		Query:        ragQuery,
		RAGChunks:    ragContext.chunks,
		RAGFallbacks: ragContext.fallbacks,
		RecallChunks: userCtx.RecallChunks,
		Memories:     userCtx.Memory,
	})
	s.applyContextArtifactRetention(artifacts)
	if !input.DeferContextArtifactPersistence && len(artifacts) > 0 {
		if err := s.repo.CreateContextArtifacts(ctx, artifacts); err != nil {
			if s.logger != nil {
				s.logger.Warn("context_artifact_persist_failed", String("trace_id", s.traceID(ctx)), String("thread_id", input.RunInput.Thread.ID), String("projection_id", input.UserMessage.Projection.ID), Error(err))
			}
			artifacts = nil
		}
	}
	userCtx.CurrentArtifacts = artifacts
	return artifacts
}

func (s *Engine) resolveRunSkillPrompts(ctx context.Context, input PromptBuildInput) (*skillPrompts, error) {
	skillPrompts, err := s.resolveSkillPrompts(ctx, input.RunInput)
	if err != nil {
		return nil, err
	}
	if input.Trace == nil || skillPrompts == nil {
		return skillPrompts, nil
	}
	skillTitles := skillPromptTitles(skillPrompts.Skills)
	input.Trace.appendProcessSection(
		fmt.Sprintf("已提供 %d 个 Skill 上下文", len(skillPrompts.Skills)),
		formatTraceStep("Skill", fmt.Sprintf("本轮已加载 Skill：%s。包含 SKILL.md 内容，相关时使用。", strings.Join(skillTitles, "、"))),
		map[string]interface{}{
			processTracePayloadStage: map[string]interface{}{
				valueKindAFE46F24:   "skill_context",
				valueStatusEA3605E1: messageTraceStatusStreaming,
			},
			"skill_count":    len(skillPrompts.Skills),
			"skill_refs":     skillPromptRefs(skillPrompts.Skills),
			"skill_titles":   skillTitles,
			"skill_triggers": skillPromptTriggers(skillPrompts.Skills),
		},
	)
	return skillPrompts, nil
}

func (s *Engine) buildRunPromptRequest(
	ctx context.Context,
	input PromptBuildInput,
	cfg Config,
	base runtimeBaseContextResult,
	stableAttachments []AttachmentInput,
	skillPrompts *skillPrompts,
) PromptBuildResult {
	toolRuntime := s.resolveSelectedToolRuntime(ctx, input.RunInput.Actor, input.RunInput.SelectedToolKeys)
	promptPlan := buildPromptPlan(ctx, promptPlanInput{
		BaseMessages:      base.llmMessages,
		StableAttachments: stableAttachments,
		DynamicContext:    base.userCtx,
		SkillPrompts:      skillPrompts,
		ToolRuntime:       toolRuntime,
		Config:            cfg,
		Actor:             input.RunInput.Actor,
		OpenFileContent:   s.openPromptFileContent,
	})
	llmMessages := promptPlan.Messages
	routeConfig := s.runtimeRouteConfig(input.Route)
	filteredOptions := filterModelOptions(input.RunInput.Options, input.Route.Protocol, modelOptionPolicyConfig{
		Mode:                  cfg.Execution.ModelOptions.Mode,
		AllowedPathsJSON:      cfg.Execution.ModelOptions.AllowedPaths,
		DeniedPathsJSON:       cfg.Execution.ModelOptions.DeniedPaths,
		ModelCapabilitiesJSON: input.Route.ModelCapabilitiesJSON,
	})
	result := newRunPromptBuildResult(input, cfg, promptPlan, llmMessages, routeConfig, filteredOptions, stableAttachments, base.prefixMemories, toolRuntime)
	recordRunPromptTrace(&result, input)
	return result
}

func (s *Engine) runtimeRouteConfig(route *LLMRoute) RouteConfig {
	attributionReferer, attributionTitle := s.llmAttribution()
	return RouteConfig{
		Protocol:           route.Protocol,
		Endpoint:           routeEndpoint(route),
		UpstreamModel:      route.UpstreamModel,
		AttributionReferer: attributionReferer,
		AttributionTitle:   attributionTitle,
	}
}

func newRunPromptBuildResult(
	input PromptBuildInput,
	cfg Config,
	promptPlan PromptPlan,
	llmMessages []Message,
	routeConfig RouteConfig,
	filteredOptions map[string]interface{},
	stableAttachments []AttachmentInput,
	prefixMemories []MemoryItem,
	toolRuntime selectedToolRuntime,
) PromptBuildResult {
	generateInput := GenerateInput{
		RequestID: strings.TrimSpace(input.RunInput.RequestID),
		Thread:    input.RunInput.Thread,
		Messages:  llmMessages,
		Tools:     toolRuntime.definitions,
		Options:   filteredOptions,
	}
	applyOpenAIResponsesInstructions(input.Route, routeConfig.Endpoint, &generateInput)
	statefulContextConfig := buildPromptContextConfigSignature(cfg)
	statefulContextState := buildPromptContextStateSignature(stableAttachments, prefixMemories)
	statefulPrefixFingerprint := buildPromptStateFingerprint(promptStateFingerprintInput{
		Protocol:          input.Route.Protocol,
		Endpoint:          routeConfig.Endpoint,
		UpstreamID:        input.Route.UpstreamID,
		UpstreamModel:     input.Route.UpstreamModel,
		PlatformModelName: input.RunInput.PlatformModelName,
		ContextConfig:     statefulContextConfig,
		ContextState:      statefulContextState,
		Messages:          promptStatePrefixMessages(llmMessages),
		Tools:             toolRuntime.definitions,
		Options:           filteredOptions,
	})
	return PromptBuildResult{
		promptPlan:                   promptPlan,
		llmMessages:                  llmMessages,
		fullLLMMessages:              llmMessages,
		routeConfig:                  routeConfig,
		generateInput:                generateInput,
		filteredOptions:              filteredOptions,
		toolRuntime:                  toolRuntime,
		estimatedPromptTokens:        estimatePromptTokens(llmMessages),
		stableFullContextAttachments: stableAttachments,
		prefixMemories:               prefixMemories,
		statefulContextConfig:        statefulContextConfig,
		statefulContextState:         statefulContextState,
		statefulPrefixFingerprint:    statefulPrefixFingerprint,
		// A v3 Text Run owns a self-contained immutable context snapshot. It
		// must never depend on an upstream response chain that cannot be
		// replayed after a crash or provider reroute.
		statefulDecision: statefulResponseDecision{DisabledReason: "self_contained_text_run_snapshot"},
	}
}

func recordRunPromptTrace(result *PromptBuildResult, input PromptBuildInput) {
	promptMode := runtimePromptMode(result.generateInput)
	result.initialPromptShape = summarizePromptShape(
		promptMode,
		result.generateInput.Messages,
		result.fullLLMMessages,
		result.generateInput.PreviousResponseID,
	)
	if input.Trace != nil {
		input.Trace.recordPromptTrace(buildMessagePromptTrace(messagePromptTraceInput{
			Plan:               result.promptPlan.Trace,
			Mode:               promptMode,
			PromptFingerprint:  result.statefulPrefixFingerprint,
			StatefulDecision:   result.statefulDecision,
			SentMessages:       result.generateInput.Messages,
			FullMessages:       result.fullLLMMessages,
			PreviousResponseID: result.generateInput.PreviousResponseID,
		}))
	}
	input.RunSpan.SetAttributes(promptShapeTraceAttributes("conversation.prompt", result.initialPromptShape)...)
}

func runtimePromptMode(input GenerateInput) string {
	if strings.TrimSpace(input.PreviousResponseID) != "" {
		return valueStatefulCE750CD9
	}
	return valueFull8613571D
}
