package agentruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	model "github.com/orz-i/Gaoge/sdk/go/agent-runtime/domain"
)

type environmentResolverTestStub struct {
	profile *EnvironmentProfile
}

func TestRunQueueFreezesExactAgentManifestRevision(t *testing.T) {
	service, actor, thread, _, environmentRef := freezeEmptyQueueCapabilities(t)
	manifest := model.AgentManifest{
		ManifestID: "agent-queue", Revision: 3, TenantID: actor.TenantID, Name: "Queued Agent", Status: model.AgentManifestStatusActive,
		ExecutionMode: TextRunExecutionModeDirect, ToolKeys: []string{}, SkillRefs: []model.ResourceRef{},
	}
	service.repo = &agentRuntimeTestStore{manifests: map[string]model.AgentManifest{manifest.ManifestID: manifest}}
	frozen, err := service.freezeRunQueueRequest(t.Context(), actor, thread, RunQueueRequest{
		Input: RunQueueInput{Content: "queued agent goal"}, Environment: environmentRef,
		AgentManifest: model.ResourceRef{Kind: model.AgentManifestKind, ID: manifest.ManifestID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if frozen.AgentManifest.Revision != "3" || frozen.ExecutionMode != TextRunExecutionModeDirect {
		t.Fatalf("frozen Agent request = %#v", frozen)
	}
	if frozen.ToolKeys == nil || frozen.SkillRefs == nil || !service.queuedCapabilitiesUnchanged(t.Context(), actor, frozen) {
		t.Fatalf("frozen Agent capabilities are not dispatchable: %#v", frozen)
	}
}

type queueToolCatalogStub struct {
	definitionVersion string
}

func (s *queueToolCatalogStub) DefaultToolKeys(context.Context, string, string, string) ([]string, error) {
	return nil, nil
}

func (s *queueToolCatalogStub) ResolveAvailable(_ context.Context, _ model.ActorRef, keys []string, _, _, modelName string) ([]ResolvedTool, []string, error) {
	result := make([]ResolvedTool, 0, len(keys))
	for _, key := range keys {
		result = append(result, ResolvedTool{ToolKey: key, ProviderKind: "builtin", ProviderKey: "test", ModelName: modelName, OriginalName: key, DefinitionVersion: s.definitionVersion, InputSchema: json.RawMessage(`{"type":"object"}`), ExecutionMode: "local_dispatch", ApprovalCapability: "none", SideEffectLevel: "read"})
	}
	return result, nil, nil
}

type queueSkillResolverStub struct {
	updatedAt time.Time
}

func (s *queueSkillResolverStub) ResolveAvailable(_ context.Context, _ model.ActorRef, ref model.ResourceRef) (*Skill, error) {
	return &Skill{Ref: ref, Title: "Queue skill", Markdown: "instructions", Enabled: true, UpdatedAt: s.updatedAt}, nil
}

func testSkillRef(id string) model.ResourceRef {
	return model.ResourceRef{Kind: ResourceKindSkill, ID: id}
}

func (s environmentResolverTestStub) ResolveAvailableEnvironmentProfile(context.Context, model.ActorRef, model.ResourceRef) (*EnvironmentProfile, error) {
	return s.profile, nil
}

func (s environmentResolverTestStub) ResolveBuiltinEnvironmentProfile(context.Context, string) (*EnvironmentProfile, error) {
	return s.profile, nil
}

const (
	valueAlways57698B05 = "always"
	activationOptional  = "optional"
	defaultModelName    = "default-model"
)

func TestStoryEnvironmentRequiresStoryWorkspace(t *testing.T) {
	story := &EnvironmentProfile{BindingScopes: []string{"story"}}
	workspace := &WorkspaceSnapshot{Request: ResolvedWorkspaceContext{Type: "story"}}
	if textRunEnvironmentWorkspaceCompatible(story, nil) || !textRunEnvironmentWorkspaceCompatible(story, workspace) {
		t.Fatal("story environment workspace compatibility was not enforced")
	}
	if textRunEnvironmentWorkspaceCompatible(&EnvironmentProfile{BindingScopes: []string{"general"}}, workspace) {
		t.Fatal("general environment accepted a story workspace")
	}
}

func TestGeneralConversationRejectsStoryOverride(t *testing.T) {
	service := &Engine{}
	override := &WorkspaceRequest{SchemaVersion: 7, Type: "story"}
	_, found, err := service.compileTextRunWorkspace(t.Context(), StartTextRunInput{Actor: model.ActorRef{TenantID: valueTenant, ActorID: valueActorRefKey}, Thread: model.ThreadRef{Kind: threadKindConversation, ID: valueThreadRefKey}, ThreadScope: conversationGeneralEnvironment, Workspace: override}, defaultModelName)
	if found || !errors.Is(err, ErrEnvironmentBindingNotAllowed) {
		t.Fatalf("general conversation workspace = found=%v err=%v", found, err)
	}
}

func TestRuntimeSelectsStrategyInsideEnvironmentPolicy(t *testing.T) {
	tests := []struct {
		name, requestedMode, defaultMode string
		allowedModes                     []string
		tools                            []effectiveRunToolPolicy
		goal, want, reason               string
		wantErr                          bool
	}{
		{name: "environment default plan", defaultMode: TextRunExecutionModePlan, allowedModes: []string{TextRunExecutionModePlan}, want: TextRunStrategyPlanned, reason: textRunStrategyReasonEnvironmentDefault},
		{name: "environment default direct", defaultMode: TextRunExecutionModeDirect, allowedModes: []string{TextRunExecutionModeDirect}, tools: []effectiveRunToolPolicy{{ApprovalMode: valueAlways57698B05}}, want: TextRunStrategyDirect, reason: textRunStrategyReasonEnvironmentDefault},
		{name: "auto single mode", defaultMode: TextRunExecutionModeAuto, allowedModes: []string{TextRunExecutionModePlan}, want: TextRunStrategyPlanned, reason: textRunStrategyReasonEnvironmentSingleMode},
		{name: "auto simple", defaultMode: TextRunExecutionModeAuto, allowedModes: []string{TextRunExecutionModeDirect, TextRunExecutionModePlan}, want: TextRunStrategyDirect, reason: textRunStrategyReasonAutoSimple},
		{name: "auto approval", defaultMode: TextRunExecutionModeAuto, allowedModes: []string{TextRunExecutionModeDirect, TextRunExecutionModePlan}, tools: []effectiveRunToolPolicy{{ApprovalMode: valueAlways57698B05}}, want: TextRunStrategyPlanned, reason: textRunStrategyReasonAutoApprovalRequired},
		{name: "auto plan intent", defaultMode: TextRunExecutionModeAuto, allowedModes: []string{TextRunExecutionModeDirect, TextRunExecutionModePlan}, goal: "制定一个多步骤计划", want: TextRunStrategyPlanned, reason: textRunStrategyReasonAutoPlanIntent},
		{name: "auto direct intent", defaultMode: TextRunExecutionModeAuto, allowedModes: []string{TextRunExecutionModeDirect, TextRunExecutionModePlan}, goal: "不要制定计划，直接回答", want: TextRunStrategyDirect, reason: textRunStrategyReasonAutoDirectIntent},
		{name: "requested direct overrides approval", requestedMode: TextRunExecutionModeDirect, defaultMode: TextRunExecutionModeAuto, allowedModes: []string{TextRunExecutionModeDirect, TextRunExecutionModePlan}, tools: []effectiveRunToolPolicy{{ApprovalMode: valueAlways57698B05}}, want: TextRunStrategyDirect, reason: textRunStrategyReasonRequestedDirect},
		{name: "requested plan", requestedMode: TextRunExecutionModePlan, defaultMode: TextRunExecutionModeAuto, allowedModes: []string{TextRunExecutionModeDirect, TextRunExecutionModePlan}, want: TextRunStrategyPlanned, reason: textRunStrategyReasonRequestedPlan},
		{name: "requested plan forbidden", requestedMode: TextRunExecutionModePlan, defaultMode: TextRunExecutionModeDirect, allowedModes: []string{TextRunExecutionModeDirect}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			goal := test.goal
			if goal == "" {
				goal = "answer briefly"
			}
			got, reason, _, err := resolveTextRunStrategy(test.requestedMode, test.defaultMode, test.allowedModes, goal, test.tools)
			if test.wantErr {
				if !errors.Is(err, ErrExecutionModeNotAllowed) {
					t.Fatalf("resolveTextRunStrategy() error = %v", err)
				}
				return
			}
			if err != nil || got != test.want || reason != test.reason {
				t.Fatalf("resolveTextRunStrategy() = (%q, %q, %v), want (%q, %q, nil)", got, reason, err, test.want, test.reason)
			}
		})
	}
}

func TestDefaultUnavailableCapabilitiesAreDiagnosedWithoutFailing(t *testing.T) {
	environment := &EnvironmentProfile{
		Tools:  []EnvironmentToolPolicy{{ToolKey: "default.offline", ActivationMode: "default", Available: false}},
		Skills: []EnvironmentSkillPolicy{{SkillRef: testSkillRef("9"), ActivationMode: "default", Available: false}},
	}
	tools, err := compileEnvironmentToolSelection(nil, environment)
	if err != nil || len(tools.StrictKeys) != 0 || len(tools.DefaultKeys) != 0 || len(tools.UnavailableDefaultKeys) != 1 {
		t.Fatalf("tool selection = %#v, err=%v", tools, err)
	}
	skills, unavailable, err := resolveEnvironmentSkillSelectionWithDiagnostics(nil, environment)
	if err != nil || len(skills) != 0 || len(unavailable) != 1 || unavailable[0] != testSkillRef("9") {
		t.Fatalf("skill selection=%v unavailable=%v err=%v", skills, unavailable, err)
	}
	selectedTools := []string{"default.offline"}
	if _, err = compileEnvironmentToolSelection(&selectedTools, environment); !errors.Is(err, ErrRunToolUnavailable) {
		t.Fatalf("explicit unavailable tool error = %v", err)
	}
}

func TestResolvedToolsKeepProviderParametersSeparateFromEnvironmentTools(t *testing.T) {
	environment := &EnvironmentProfile{Tools: []EnvironmentToolPolicy{{ToolKey: "mcp.allowed", Available: true}}}
	if !resolvedToolsRespectEnvironmentBoundary([]effectiveRunToolPolicy{{ToolKey: "openai.web_search", ExecutionMode: valueProviderHosted7ED91AC1}}, environment) {
		t.Fatal("explicit provider-hosted parameter must not require an Environment binding")
	}
	if !resolvedToolsRespectEnvironmentBoundary([]effectiveRunToolPolicy{{ToolKey: "mcp.allowed", ExecutionMode: valueLocalDispatch71FF6D47}}, environment) {
		t.Fatal("bound local tool was rejected")
	}
	if resolvedToolsRespectEnvironmentBoundary([]effectiveRunToolPolicy{{ToolKey: "mcp.unbound", ExecutionMode: valueLocalDispatch71FF6D47}}, environment) {
		t.Fatal("unbound local tool escaped the Environment boundary")
	}
}

func TestEnvironmentModelSelectionCannotEscapeAllowlist(t *testing.T) {
	environment := &EnvironmentProfile{Models: []EnvironmentModelPolicy{
		{PlatformModelName: "allowed-default", IsDefault: true, Available: true},
		{PlatformModelName: "allowed-choice", Selectable: true, Available: true},
	}}
	if selected, err := selectEnvironmentModel(environment, ""); err != nil || selected != "allowed-default" {
		t.Fatalf("default model selection = %q, %v", selected, err)
	}
	if _, err := selectEnvironmentModel(environment, "outside"); !errors.Is(err, ErrEnvironmentModelNotAuthorized) {
		t.Fatalf("outside model error = %v", err)
	}
}

func TestEnvironmentModelSelectionReportsLifecycleFailures(t *testing.T) {
	if _, err := selectEnvironmentModel(&EnvironmentProfile{}, ""); !errors.Is(err, ErrEnvironmentModelUnconfigured) {
		t.Fatalf("empty environment error = %v", err)
	}
	defaultUnavailable := &EnvironmentProfile{Models: []EnvironmentModelPolicy{
		{PlatformModelName: "offline-default", IsDefault: true},
		{PlatformModelName: "available-choice", Selectable: true, Available: true},
	}}
	if _, err := selectEnvironmentModel(defaultUnavailable, ""); !errors.Is(err, ErrEnvironmentDefaultUnavailable) {
		t.Fatalf("unavailable default error = %v", err)
	}
	if _, err := selectEnvironmentModel(defaultUnavailable, "offline-default"); !errors.Is(err, ErrEnvironmentModelNotAccessible) {
		t.Fatalf("inaccessible model error = %v", err)
	}
}

func TestEnvironmentSkillsAddRequiredAndRejectOutsideSelection(t *testing.T) {
	environment := &EnvironmentProfile{Skills: []EnvironmentSkillPolicy{
		{SkillRef: testSkillRef("1"), ActivationMode: valueRequired466769C7, Available: true},
		{SkillRef: testSkillRef("2"), ActivationMode: "default", Available: true},
		{SkillRef: testSkillRef("3"), ActivationMode: activationOptional, Available: true},
	}}
	selected := []model.ResourceRef{testSkillRef("3")}
	got, err := resolveEnvironmentSkillSelection(&selected, environment)
	if err != nil || len(got) != 2 || got[0] != testSkillRef("1") || got[1] != testSkillRef("3") {
		t.Fatalf("effective skills = %#v, %v", got, err)
	}
	outside := []model.ResourceRef{testSkillRef("99")}
	if _, err = resolveEnvironmentSkillSelection(&outside, environment); !errors.Is(err, ErrRunSkillUnavailable) {
		t.Fatalf("outside skill error = %v", err)
	}
}

func TestRunQueueFreezesThreadDefaults(t *testing.T) {
	thread := &ThreadSnapshot{Thread: model.ThreadRef{Kind: threadKindConversation, ID: valueThreadRefKey}, Environment: model.ResourceRef{Kind: resourceKindEnvironment, ID: "7", Revision: "3"}, DefaultModel: "conversation-model", BindingScope: conversationGeneralEnvironment}
	emptyTools, emptySkills := []string{}, []model.ResourceRef{}
	frozen := freezeRunQueueRequest(thread, RunQueueRequest{ToolKeys: &emptyTools, SkillRefs: &emptySkills})
	if frozen.Model != thread.DefaultModel || frozen.Environment != thread.Environment || frozen.ThreadScope != thread.BindingScope {
		t.Fatalf("frozen request = %#v", frozen)
	}
}

func TestRunQueueRejectsChangedCapabilityDefinitions(t *testing.T) {
	toolCatalog := &queueToolCatalogStub{definitionVersion: "v1"}
	skillResolver := &queueSkillResolverStub{updatedAt: time.Unix(1, 0)}
	environment := &EnvironmentProfile{Ref: model.ResourceRef{Kind: resourceKindEnvironment, ID: "7", Revision: "3"}, Revision: 3, BindingScopes: []string{conversationGeneralEnvironment}, DefaultMode: TextRunExecutionModeAuto, AllowedModes: []string{TextRunExecutionModeDirect, TextRunExecutionModePlan}, Models: []EnvironmentModelPolicy{{PlatformModelName: defaultModelName, IsDefault: true, Available: true}}, Tools: []EnvironmentToolPolicy{{ToolKey: "allowed-tool", ActivationMode: activationOptional, Available: true}}, Skills: []EnvironmentSkillPolicy{{SkillRef: testSkillRef("2"), ActivationMode: activationOptional, Available: true}}}
	service := &Engine{cfg: StaticConfigProvider(Config{}), environmentProfiles: environmentResolverTestStub{profile: environment}, toolCatalog: toolCatalog, skillResolver: skillResolver}
	actor := model.ActorRef{TenantID: valueTenant, ActorID: valueActorRefKey}
	thread := &ThreadSnapshot{Thread: model.ThreadRef{Kind: threadKindConversation, ID: valueThreadRefKey}, Environment: model.ResourceRef{Kind: resourceKindEnvironment, ID: "7", Revision: "3"}, DefaultModel: defaultModelName, BindingScope: conversationGeneralEnvironment}
	tools, skills := []string{"allowed-tool"}, []model.ResourceRef{testSkillRef("2")}
	frozen, err := service.freezeRunQueueRequest(t.Context(), actor, thread, RunQueueRequest{Input: RunQueueInput{Content: "queued goal"}, ToolKeys: &tools, SkillRefs: &skills})
	if err != nil || !service.queuedCapabilitiesUnchanged(t.Context(), actor, frozen) {
		t.Fatalf("frozen capabilities are not stable: request=%#v err=%v", frozen, err)
	}
	toolCatalog.definitionVersion = "v2"
	if service.queuedCapabilitiesUnchanged(t.Context(), actor, frozen) {
		t.Fatal("changed tool definition was accepted")
	}
	toolCatalog.definitionVersion = "v1"
	skillResolver.updatedAt = time.Unix(2, 0)
	if service.queuedCapabilitiesUnchanged(t.Context(), actor, frozen) {
		t.Fatal("changed skill definition was accepted")
	}
}

func freezeEmptyQueueCapabilities(t *testing.T) (*Engine, model.ActorRef, *ThreadSnapshot, RunQueueRequest, model.ResourceRef) {
	t.Helper()
	environment := &EnvironmentProfile{
		Ref:           model.ResourceRef{Kind: resourceKindEnvironment, ID: "7", Revision: "3"},
		Revision:      3,
		BindingScopes: []string{conversationGeneralEnvironment},
		DefaultMode:   TextRunExecutionModeAuto,
		AllowedModes:  []string{TextRunExecutionModeDirect, TextRunExecutionModePlan},
		Models:        []EnvironmentModelPolicy{{PlatformModelName: defaultModelName, IsDefault: true, Available: true}},
	}
	service := &Engine{cfg: StaticConfigProvider(Config{}), environmentProfiles: environmentResolverTestStub{profile: environment}}
	actor := model.ActorRef{TenantID: valueTenant, ActorID: valueActorRefKey}
	thread := &ThreadSnapshot{
		Thread:       model.ThreadRef{Kind: threadKindConversation, ID: valueThreadRefKey},
		Environment:  model.ResourceRef{Kind: resourceKindEnvironment, ID: "7"},
		DefaultModel: defaultModelName,
		BindingScope: conversationGeneralEnvironment,
	}
	frozen, err := service.freezeRunQueueRequest(t.Context(), actor, thread, RunQueueRequest{Input: RunQueueInput{Content: "queued goal"}})
	if err != nil {
		t.Fatal(err)
	}
	return service, actor, thread, frozen, environment.Ref
}

func TestRunQueueFreezesEmptyCapabilitiesAsArrays(t *testing.T) {
	_, _, _, frozen, environmentRef := freezeEmptyQueueCapabilities(t)
	if frozen.Environment != environmentRef {
		t.Fatalf("environment = %#v, want canonical ref %#v", frozen.Environment, environmentRef)
	}
	if frozen.ToolKeys == nil || *frozen.ToolKeys == nil {
		t.Fatalf("empty frozen tools must be a non-nil array: %#v", frozen.ToolKeys)
	}
	if frozen.SkillRefs == nil || *frozen.SkillRefs == nil {
		t.Fatalf("empty frozen skills must be a non-nil array: %#v", frozen.SkillRefs)
	}
}

func TestRunQueueEmptyCapabilitiesSurvivePersistence(t *testing.T) {
	service, actor, thread, frozen, _ := freezeEmptyQueueCapabilities(t)
	encoded, err := json.Marshal(frozen)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"toolKeys":null`) {
		t.Fatalf("frozen request contains null tool selection: %s", encoded)
	}
	if strings.Contains(string(encoded), `"skillRefs":null`) {
		t.Fatalf("frozen request contains null skill selection: %s", encoded)
	}
	var decoded RunQueueRequest
	if err = json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if !queuedThreadSnapshotUnchanged(thread, decoded) {
		t.Fatal("canonical revision must not look like a changed host binding when the host only exposes identity")
	}
	if !service.queuedCapabilitiesUnchanged(t.Context(), actor, decoded) {
		t.Fatal("empty frozen capabilities must survive persistence and dispatch")
	}
}

func TestQueuedThreadSnapshotUsesEnvironmentIdentityAndOptionalRevision(t *testing.T) {
	request := RunQueueRequest{
		Environment:    model.ResourceRef{Kind: resourceKindEnvironment, ID: "7", Revision: "3"},
		ThreadModel:    defaultModelName,
		ThreadProvider: "internal",
		ThreadScope:    conversationGeneralEnvironment,
	}
	thread := &ThreadSnapshot{
		Environment:   model.ResourceRef{Kind: resourceKindEnvironment, ID: "7"},
		DefaultModel:  defaultModelName,
		ModelProvider: "internal",
		BindingScope:  conversationGeneralEnvironment,
	}
	if !queuedThreadSnapshotUnchanged(thread, request) {
		t.Fatal("environment identity without a host revision must accept the frozen canonical revision")
	}
	thread.Environment.Revision = "4"
	if queuedThreadSnapshotUnchanged(thread, request) {
		t.Fatal("an explicit changed host environment revision must be rejected")
	}
}

func TestTextRunConfigurationRejectsChangedQueuedEnvironmentRevision(t *testing.T) {
	environment := &EnvironmentProfile{Ref: model.ResourceRef{Kind: resourceKindEnvironment, ID: "7", Revision: "4"}, Revision: 4, BindingScopes: []string{conversationGeneralEnvironment}, DefaultMode: TextRunExecutionModeAuto, AllowedModes: []string{TextRunExecutionModeDirect, TextRunExecutionModePlan}, Models: []EnvironmentModelPolicy{{PlatformModelName: defaultModelName, IsDefault: true, Available: true}}}
	service := &Engine{cfg: StaticConfigProvider(Config{}), environmentProfiles: environmentResolverTestStub{profile: environment}}
	_, err := service.prepareTextRunConfiguration(t.Context(), StartTextRunInput{
		Actor: model.ActorRef{TenantID: valueTenant, ActorID: valueActorRefKey}, Thread: model.ThreadRef{Kind: threadKindConversation, ID: valueThreadRefKey}, Environment: model.ResourceRef{Kind: resourceKindEnvironment, ID: "7", Revision: "3"},
		PlatformModelName: defaultModelName,
	}, "queued goal")
	if !errors.Is(err, ErrRunEnvironmentChanged) {
		t.Fatalf("revision mismatch error = %v", err)
	}
}
