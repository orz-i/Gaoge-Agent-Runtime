package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"time"

	interactionadapter "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/adapters/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/interaction"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/plugin"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const acceptanceVersion = "task-outcome-acceptance-v2"
const acceptanceModelLimit = 8
const acceptanceToolLimit = 8
const acceptanceInputBytes = 64 << 10

var acceptanceRevisionPattern = regexp.MustCompile(`^[0-9a-f]{40,64}$`)

const acceptanceApprovalPolicyVersion = "local-approve-or-reject-v1"

type acceptanceResult struct {
	Case                string          `json:"case"`
	Family              string          `json:"family"`
	TaskHash            string          `json:"taskHash,omitempty"`
	AgentStarted        bool            `json:"agentStarted"`
	AgentCompleted      bool            `json:"agentCompleted"`
	OutcomePassed       bool            `json:"outcomePassed"`
	Checks              map[string]bool `json:"checks"`
	ModelStepsConsumed  int             `json:"modelStepsConsumed"`
	ToolCallsConsumed   int             `json:"toolCallsConsumed"`
	DurationMs          int64           `json:"durationMs"`
	GenerationTokens    *int64          `json:"completeGenerationTokens"`
	ArtifactSHA256      string          `json:"artifactSHA256,omitempty"`
	Failure             string          `json:"failure,omitempty"`
	ApprovalIsSimulated bool            `json:"approvalIsSimulated,omitempty"`
}

type acceptanceReport struct {
	Version                   string             `json:"version"`
	Mode                      string             `json:"mode"`
	FixtureOnly               bool               `json:"fixtureOnly"`
	Requested                 int                `json:"requested"`
	Cases                     []string           `json:"cases"`
	Attempted                 int                `json:"attempted"`
	Processed                 int                `json:"processed"`
	Provider                  string             `json:"provider"`
	Model                     string             `json:"model"`
	OutputLimit               int64              `json:"maxOutputTokens"`
	ProfileHash               string             `json:"modelProfileHash,omitempty"`
	EndpointHash              string             `json:"endpointHash,omitempty"`
	GoVersion                 string             `json:"goVersion"`
	SourceRevision            string             `json:"sourceRevision,omitempty"`
	SourceDirty               *bool              `json:"sourceDirty,omitempty"`
	ModelLimit                int                `json:"maxModelCalls"`
	ToolLimit                 int                `json:"maxToolCalls"`
	InputBytes                int                `json:"maxInputBytes"`
	Context                   int64              `json:"contextTokens"`
	Attempts                  int                `json:"maxAttemptsPerCall"`
	TaskFamilies              []string           `json:"taskFamilies"`
	TaskFamilyPlannedCounts   map[string]int     `json:"taskFamilyPlannedCounts"`
	TaskFamilyProcessedCounts map[string]int     `json:"taskFamilyProcessedCounts"`
	Results                   []acceptanceResult `json:"results"`
}

type acceptanceTask struct {
	name, goal, directory    string
	answer                   string
	readDocuments            map[string]bool
	documents                map[string]string
	codeFixture              acceptanceCodeFixture
	expectedQuantity         int
	expectedDelivery         string
	expectedSource           string
	approvedWrites           int
	approvalDeniedReceipt    bool
	publicTestsRan           bool
	publicTestsPassedCurrent bool
	publicTestsSourceHash    [32]byte
	lastTestOutput           string
}

func runAcceptanceCommand(ctx context.Context, config settings, selected string, allowLive bool, output io.Writer) int {
	if selected == "" {
		selected = "all"
	}
	cases := []string{selected}
	if selected == "all" {
		cases = []string{"code", "code-negative", "code-degenerate", "documents", "documents-distractors", "approval", "approval-denied"}
	}
	for _, name := range cases {
		if !validAcceptanceCase(name) {
			return configurationError(output)
		}
	}
	live := config.provider != ""
	if live {
		if !allowLive {
			return configurationError(output) // no key lookup, model construction or network
		}
		if len(cases) != 1 {
			return configurationError(output) // live acceptance is one fixed task per invocation
		}
		config.mode = "live"
		if config.resolve() != nil {
			return configurationError(output)
		}
	} else if allowLive || config.modelID != "" || config.contextTokens != 0 || config.outputTokens != 0 {
		return configurationError(output)
	}
	if config.outputTokens <= 0 {
		config.outputTokens = 1024
	}
	if !live {
		config.contextTokens = 131072
	}
	sourceRevision, sourceDirty := acceptanceSourceBuild()
	if live && (!acceptanceRevisionPattern.MatchString(sourceRevision) || sourceDirty == nil || *sourceDirty) {
		return configurationError(output)
	}
	report := acceptanceReport{Version: acceptanceVersion, Mode: "fixture", FixtureOnly: !live, Requested: len(cases),
		Cases:    append([]string(nil), cases...),
		Provider: "fixture", Model: "acceptance-fixture", OutputLimit: config.outputTokens, GoVersion: runtime.Version(),
		ModelLimit: acceptanceModelLimit, ToolLimit: acceptanceToolLimit, InputBytes: acceptanceInputBytes, Context: config.contextTokens, Attempts: 1,
		TaskFamilies: acceptanceFamilies(cases), TaskFamilyPlannedCounts: acceptanceFamilyCounts(cases),
		TaskFamilyProcessedCounts: make(map[string]int), SourceRevision: sourceRevision, SourceDirty: sourceDirty}
	if live {
		report.Mode = "provider_live"
		report.Provider, report.Model = config.provider, config.routedModel()
		var endpointErr error
		report.EndpointHash, endpointErr = acceptanceEndpointHash(config)
		if endpointErr != nil {
			return configurationError(output)
		}
		profiles, profileHash, err := acceptanceModelProfiles(config)
		if err != nil {
			return configurationError(output)
		}
		config.modelProfiles = profiles
		report.ProfileHash = profileHash
	}
	exit := 0
	for _, name := range cases {
		var client model.Client = acceptanceFixtureModel{task: name}
		selectedModel := "acceptance-fixture"
		if live {
			var err error
			client, err = config.newModel()
			if err != nil {
				report.Results = append(report.Results, acceptanceResult{Case: name, Family: acceptanceFamily(name), Checks: map[string]bool{},
					ApprovalIsSimulated: acceptanceFamily(name) == "approval-control", Failure: "provider_configuration_failed"})
				exit = 1
				break
			}
			selectedModel = config.routedModel()
		}
		taskCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		result := runAcceptanceTask(taskCtx, name, client, selectedModel, report.ProfileHash, report.EndpointHash, config.contextTokens, config.outputTokens)
		cancel()
		report.Results = append(report.Results, result)
		if !result.OutcomePassed {
			exit = 1
			break // never spend on later live cases after a failure
		}
	}
	report.Processed = len(report.Results)
	for _, result := range report.Results {
		if result.AgentStarted {
			report.Attempted++
		}
		if result.Family != "" {
			report.TaskFamilyProcessedCounts[result.Family]++
		}
	}
	if err := validateAcceptanceReport(report); err != nil {
		// A local report inconsistency cannot be published as task evidence.
		return configurationError(output)
	}
	if json.NewEncoder(output).Encode(report) != nil {
		return 1
	}
	return exit
}

func acceptanceSourceBuild() (string, *bool) {
	build, ok := debug.ReadBuildInfo()
	if !ok {
		return "", nil
	}
	var revision string
	var dirty *bool
	for _, setting := range build.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			value := setting.Value == "true"
			dirty = &value
		}
	}
	return revision, dirty
}

func acceptanceModelProfiles(config settings) (model.StaticModelProfiles, string, error) {
	source, err := config.hostModelProfileSource()
	if err != nil {
		return nil, "", err
	}
	if source == nil {
		return nil, "", errors.New("acceptance model profiles are required")
	}
	profiles := model.StaticModelProfiles(source.SnapshotModelProfiles()).SnapshotModelProfiles()
	profile, ok := profiles[config.modelID]
	if !ok {
		return nil, "", errors.New("the exact acceptance model has no Host-certified model profile")
	}
	encoded, err := json.Marshal(profile)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(encoded)
	return profiles, hex.EncodeToString(digest[:]), nil
}

func acceptanceEndpointHash(config settings) (string, error) {
	endpoint := strings.TrimSpace(config.endpoint)
	if endpoint == "" {
		switch config.provider {
		case "openai":
			endpoint = "https://api.openai.com/v1/responses"
		case "anthropic":
			endpoint = "https://api.anthropic.com/v1/messages"
		default:
			return "", errors.New("unknown provider endpoint")
		}
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" ||
		(parsed.Scheme != "https" && parsed.Scheme != "http") || len(endpoint) > 2048 {
		return "", errors.New("invalid acceptance endpoint")
	}
	digest := sha256.Sum256([]byte(endpoint))
	return hex.EncodeToString(digest[:]), nil
}

func runAcceptanceTask(ctx context.Context, name string, client model.Client, selectedModel, profileHash, endpointHash string, contextTokens, outputTokens int64) (result acceptanceResult) {
	started := time.Now()
	defer func() { result.DurationMs = time.Since(started).Milliseconds() }()
	if outputTokens <= 0 {
		outputTokens = 1024
	}
	result = acceptanceResult{Case: name, Family: acceptanceFamily(name), Checks: make(map[string]bool), ApprovalIsSimulated: name == "approval" || name == "approval-denied"}
	directory, err := os.MkdirTemp("", "gaoge-task-acceptance-")
	if err != nil {
		result.Failure = "workspace_unavailable"
		return result
	}
	defer func() { _ = os.RemoveAll(directory) }() // only this command's owned scratch directory
	task := &acceptanceTask{name: name, directory: directory, readDocuments: make(map[string]bool)}
	registrations, err := task.prepare()
	if err != nil {
		result.Failure = "fixture_setup_failed"
		return result
	}
	definitions := make([]tools.Definition, len(registrations))
	for index, registration := range registrations {
		definitions[index] = registration.Definition
	}
	identity, err := json.Marshal(struct {
		Version, Name, Goal, Fixtures                       string
		Tools                                               []tools.Definition
		ModelLimit, ToolLimit, Bytes                        int
		Attempts                                            int
		ContextTokens, OutputTokens                         int64
		SelectedModel, ProfileHash, EndpointHash, GoVersion string
	}{acceptanceVersion, name, task.goal,
		task.fixtureIdentity(),
		definitions, acceptanceModelLimit, acceptanceToolLimit, acceptanceInputBytes, 1, contextTokens, outputTokens, selectedModel, profileHash, endpointHash, runtime.Version()})
	if err != nil {
		result.Failure = "task_identity_unavailable"
		return result
	}
	hash := sha256.Sum256(identity)
	result.TaskHash = hex.EncodeToString(hash[:])
	if _, code := acceptanceCodeCase(name); code {
		compiled, compileErr := task.testCode(ctx, true)
		if compileErr != nil || !compiled {
			result.Failure = "fixture_toolchain_unavailable"
			return result
		}
	}
	registry, err := tools.NewRegistry(registrations)
	if err != nil {
		result.Failure = "tool_configuration_failed"
		return result
	}
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		result.Failure = "runtime_setup_failed"
		return result
	}
	approvals, err := interaction.New(rt)
	if err != nil {
		result.Failure = "approval_setup_failed"
		return result
	}
	compose := func() (*agent.Runner, error) {
		dependencies := agent.Dependencies{Runtime: rt, Model: client, Catalog: registry, Executor: registry,
			Limits: agent.Limits{MaxLLMCalls: acceptanceModelLimit, MaxToolCalls: acceptanceToolLimit},
			Execution: agent.ExecutionPolicy{Model: agent.CallPolicy{MaxAttempts: 1, MaxConcurrency: 1},
				Tool: agent.CallPolicy{MaxAttempts: 1, MaxConcurrency: 1}},
			PrepareModelRequest: func(ctx context.Context, request model.Request) (model.Request, error) {
				return acceptanceInputBound(ctx, request, contextTokens, outputTokens)
			},
		}
		if name == "approval" || name == "approval-denied" {
			dependencies.Approvals = interactionadapter.New(approvals)
			dependencies.ApprovalPolicies = []plugin.ApprovalPolicy{acceptanceApprovalPolicy{}}
		}
		return agent.NewRunner(dependencies)
	}
	loop, err := compose()
	if err != nil {
		result.Failure = "agent_setup_failed"
		return result
	}
	keys := make([]string, len(registrations))
	for index, registration := range registrations {
		keys[index] = registration.Definition.Key
	}
	request := agent.StartRequest{ID: "acceptance-" + name, RequestID: "request-" + name,
		Actor: kernel.ActorRef{TenantID: "fixture", ActorID: "operator"}, Thread: kernel.ThreadRef{Kind: "acceptance", ID: name},
		Model: selectedModel, MaxModelOutputTokens: outputTokens, Goal: task.goal, ToolKeys: keys}
	if name != "approval-denied" {
		request.RequiredToolKeys = keys
	}
	current, runErr := loop.StartRun(ctx, request)
	result.AgentStarted = current.Run.ID != ""
	if (name == "approval" || name == "approval-denied") && runErr == nil {
		result.Checks["no_write_before_approval"] = current.Run.Status == kernel.RunStatusWaitingInput && task.approvedWrites == 0
		if result.Checks["no_write_before_approval"] {
			loop, err = compose() // new Runner; original checkpoint/Run is retained
			if err == nil {
				decision := plugin.ApprovalApprove
				if name == "approval-denied" {
					decision = plugin.ApprovalReject
				}
				current, runErr = loop.ResolveApproval(ctx, current.Run.ID, current.Run.Revision, plugin.ApprovalResponse{Decision: decision})
				if name == "approval-denied" && current.Run.ID != "" {
					if view, viewErr := agent.ViewState(current); viewErr == nil {
						for _, message := range view.Messages {
							if message.Role != model.RoleTool {
								continue
							}
							var receipt struct {
								Status string `json:"status"`
							}
							task.approvalDeniedReceipt = task.approvalDeniedReceipt ||
								(json.Unmarshal([]byte(model.TextValue(message.Content)), &receipt) == nil && receipt.Status == "rejected")
						}
					}
					if runErr == nil && current.Run.Status == kernel.RunStatusRunning {
						current, runErr = loop.Resume(ctx, current.Run.ID, current.Run.Revision)
					}
				}
			} else {
				runErr = err
			}
		}
	}
	result.AgentCompleted = runErr == nil && current.Run.Status == kernel.RunStatusCompleted
	if current.Run.ID != "" {
		if current.Result != nil {
			task.answer = string(current.Result.Content)
		}
		if view, err := agent.ViewState(current); err == nil {
			result.ModelStepsConsumed = view.Budget.Usage.LLMCalls
			result.ToolCallsConsumed = view.Budget.Usage.ToolCalls
			completeUsage := len(view.ModelInvocations) > 0
			tokens := view.Budget.Usage.TotalTokens
			for _, invocation := range view.ModelInvocations {
				if invocation.Status != agent.ModelInvocationConsumed || invocation.Response.Usage == nil {
					completeUsage = false
					continue
				}
			}
			if completeUsage {
				result.GenerationTokens = &tokens
			}
		}
	}
	if name == "approval-denied" {
		result.Checks["denial_has_rejection_receipt"] = task.approvalDeniedReceipt
	}
	if !result.AgentCompleted {
		result.Failure = "agent_did_not_complete"
		return result
	}
	if name == "approval" || name == "approval-denied" {
		before := task.approvedWrites
		_, terminalErr := loop.Resume(ctx, current.Run.ID, current.Run.Revision)
		result.Checks["terminal_resume_does_not_write"] = errors.Is(terminalErr, agent.ErrRunTerminal) && task.approvedWrites == before
	}
	artifact, oracleErr := task.verify(ctx, result.Checks)
	if oracleErr != nil {
		result.Failure = "oracle_unavailable"
		return result
	}
	if artifact != nil {
		digest := sha256.Sum256(artifact)
		result.ArtifactSHA256 = hex.EncodeToString(digest[:])
	}
	result.OutcomePassed = len(result.Checks) > 0
	for _, passed := range result.Checks {
		result.OutcomePassed = result.OutcomePassed && passed
	}
	if !result.OutcomePassed {
		result.Failure = "task_outcome_failed"
	}
	return result
}

func acceptanceInputBound(ctx context.Context, request model.Request, contextTokens, outputTokens int64) (model.Request, error) {
	manager := runtimecontext.NewManager(runtimecontext.Dependencies{})
	policy := runtimecontext.Policy{MaxInputTokens: acceptanceInputBytes, MaxSerializedBytes: acceptanceInputBytes}
	assessment, err := manager.AssessModelRequest(ctx, request,
		runtimecontext.ModelWindow{ContextTokens: contextTokens, ReservedOutputTokens: outputTokens}, policy)
	if err != nil || !runtimecontext.WithinHardBudget(assessment, policy) {
		return model.Request{}, errors.New("acceptance input byte ceiling exceeded")
	}
	return request, nil
}

type acceptanceApprovalPolicy struct{}

func (acceptanceApprovalPolicy) Name() string { return "acceptance.required-approval" }
func (acceptanceApprovalPolicy) Approval(context.Context, plugin.ToolInvocation) (plugin.ApprovalRequirement, error) {
	return plugin.ApprovalRequired, nil
}

func acceptanceRegistration(key, description, schema string, execute tools.HandlerFunc) tools.Registration {
	return tools.Registration{Definition: tools.Definition{Key: key, Name: key, Description: description, InputSchema: json.RawMessage(schema)}, Handler: execute}
}

func acceptanceReceipt(request tools.ExecutionRequest, value any) (tools.ExecutionResult, error) {
	content, err := json.Marshal(value)
	if err != nil {
		return tools.ExecutionResult{}, err
	}
	return tools.ExecutionResult{Content: content, Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"}}, nil
}

func acceptanceRefusal(request tools.ExecutionRequest, code, message string) (tools.ExecutionResult, error) {
	return tools.RecoverableNoEffectResult(request.Call.ID, tools.NewRecoverableCallError(code, message, tools.ErrInvalidCall))
}

func (task *acceptanceTask) fixtureIdentity() string {
	if _, code := acceptanceCodeCase(task.name); code {
		return task.codeFixture.initial + task.codeFixture.public + task.codeFixture.heldout + "expected-source:" + task.codeFixture.expected
	}
	var evidence []byte
	for _, id := range []string{"D-01", "D-02", "D-03", "D-04"} {
		evidence = append(evidence, []byte(id+task.documents[id])...)
	}
	return string(evidence) + fmt.Sprintf("expected=%d:%s:%s;artifact=%q;approval-policy=%s;approval-denied=%t",
		task.expectedQuantity, task.expectedDelivery, task.expectedSource, "approved fixture record\n", acceptanceApprovalPolicyVersion, task.name == "approval-denied")
}

func validateAcceptanceReport(report acceptanceReport) error {
	if report.Version != acceptanceVersion || report.Requested < 1 || report.Requested > 16 || len(report.Cases) != report.Requested ||
		report.Processed != len(report.Results) || report.Processed > report.Requested || report.Attempted > report.Processed ||
		report.ModelLimit != acceptanceModelLimit || report.ToolLimit != acceptanceToolLimit || report.InputBytes != acceptanceInputBytes ||
		report.Context <= report.OutputLimit || report.OutputLimit < 1 || report.Attempts != 1 {
		return errors.New("acceptance report identity or bounds mismatch")
	}
	if report.Model == "" || report.Provider == "" || (report.Mode == "fixture") != report.FixtureOnly ||
		report.Mode != "fixture" && report.Mode != "provider_live" || report.Processed == 0 || report.GoVersion == "" || len(report.GoVersion) > 64 {
		return errors.New("invalid acceptance execution mode")
	}
	if report.Mode == "provider_live" && (report.FixtureOnly || !validAcceptanceHash(report.ProfileHash) || !validAcceptanceHash(report.EndpointHash) || report.SourceDirty == nil ||
		*report.SourceDirty || !acceptanceRevisionPattern.MatchString(report.SourceRevision)) {
		return errors.New("live report missing model/profile/source identity")
	}
	if report.SourceRevision != "" && !acceptanceRevisionPattern.MatchString(report.SourceRevision) {
		return errors.New("invalid acceptance source revision")
	}
	if report.Mode == "fixture" && (report.ProfileHash != "" || report.EndpointHash != "") {
		return errors.New("fixture report has a Provider binding")
	}
	if report.Mode == "fixture" && (report.Provider != "fixture" || report.Model != "acceptance-fixture" || report.Context != 131072 || report.OutputLimit != 1024) {
		return errors.New("fixture execution profile mismatch")
	}
	expected := acceptanceFamilies(report.Cases)
	if !equalAcceptanceStrings(report.TaskFamilies, expected) {
		return errors.New("acceptance task-family identity mismatch")
	}
	planned := make(map[string]bool, len(report.Cases))
	for _, name := range report.Cases {
		if !validAcceptanceCase(name) || planned[name] {
			return errors.New("invalid or duplicate planned acceptance case")
		}
		planned[name] = true
	}
	if !equalAcceptanceCounts(report.TaskFamilyPlannedCounts, acceptanceFamilyCounts(report.Cases)) {
		return errors.New("acceptance planned family counts mismatch")
	}
	processedCounts := make(map[string]int)
	for _, result := range report.Results {
		processedCounts[result.Family]++
	}
	if !equalAcceptanceCounts(report.TaskFamilyProcessedCounts, processedCounts) {
		return errors.New("acceptance processed family counts mismatch")
	}
	started, passed := 0, 0
	seen := make(map[string]bool)
	for index, result := range report.Results {
		if seen[result.Case] || index >= len(report.Cases) || result.Case != report.Cases[index] || !validAcceptanceCase(result.Case) || result.Family != acceptanceFamily(result.Case) || len(result.Checks) > 32 ||
			result.ModelStepsConsumed < 0 || result.ModelStepsConsumed > acceptanceModelLimit || result.ToolCallsConsumed < 0 || result.ToolCallsConsumed > acceptanceToolLimit ||
			result.DurationMs < 0 ||
			(result.GenerationTokens != nil && (*result.GenerationTokens < 0 || *result.GenerationTokens > 1<<50)) ||
			(result.ArtifactSHA256 != "" && !validAcceptanceHash(result.ArtifactSHA256)) || len(result.Failure) > 64 {
			return errors.New("invalid acceptance sample")
		}
		seen[result.Case] = true
		if result.Failure != "" && !validAcceptanceFailure(result.Failure) {
			return errors.New("invalid acceptance failure code")
		}
		if result.OutcomePassed != (result.Failure == "") {
			return errors.New("acceptance outcome and failure classification disagree")
		}
		if index < len(report.Results)-1 && !result.OutcomePassed {
			return errors.New("acceptance continued after a failed case")
		}
		if !validAcceptanceChecks(result) {
			return errors.New("acceptance checks do not match the case oracle")
		}
		if result.AgentStarted {
			started++
			if !validAcceptanceHash(result.TaskHash) {
				return errors.New("started task missing identity hash")
			}
		} else if (result.TaskHash != "" && !validAcceptanceHash(result.TaskHash)) || result.AgentCompleted || result.OutcomePassed || result.Failure == "" || len(result.Checks) != 0 {
			return errors.New("pre-Agent failure is mislabeled as an attempted task")
		}
		if !result.AgentStarted && (result.ModelStepsConsumed != 0 || result.ToolCallsConsumed != 0 || result.ArtifactSHA256 != "" || result.GenerationTokens != nil) {
			return errors.New("pre-Agent failure claims model, Tool or artifact effects")
		}
		if result.AgentCompleted && (!result.AgentStarted || result.ModelStepsConsumed == 0) {
			return errors.New("completed task has no consumed model step")
		}
		if result.Failure == "" && !result.AgentCompleted {
			return errors.New("incomplete Agent result lacks a failure classification")
		}
		if result.OutcomePassed {
			passed++
			denial := result.Case == "approval-denied"
			if !result.AgentCompleted || result.Failure != "" || !validAcceptanceHash(result.TaskHash) ||
				(denial && (result.ArtifactSHA256 != "" || result.ToolCallsConsumed != 0)) ||
				(!denial && (!validAcceptanceHash(result.ArtifactSHA256) || result.ToolCallsConsumed == 0)) {
				return errors.New("invalid successful acceptance sample")
			}
		}
		if (result.Family == "approval-control") != result.ApprovalIsSimulated {
			return errors.New("approval simulation marker mismatch")
		}
		if result.TaskHash != "" && !validAcceptanceHash(result.TaskHash) {
			return errors.New("invalid acceptance task identity")
		}
	}
	if started != report.Attempted || passed > started || report.Attempted > report.Processed {
		return errors.New("acceptance count mismatch")
	}
	if report.Processed < report.Requested && report.Results[report.Processed-1].OutcomePassed {
		return errors.New("partial report omitted a failed stopping case")
	}
	if report.Processed < report.Requested && report.Results[report.Processed-1].OutcomePassed {
		return errors.New("partial acceptance report omits its stopping failure")
	}
	return nil
}

func equalAcceptanceStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func validAcceptanceFailure(value string) bool {
	switch value {
	case "provider_configuration_failed", "workspace_unavailable", "fixture_setup_failed",
		"task_identity_unavailable", "fixture_toolchain_unavailable", "tool_configuration_failed",
		"runtime_setup_failed", "approval_setup_failed", "agent_setup_failed",
		"agent_did_not_complete", "oracle_unavailable", "task_outcome_failed":
		return true
	default:
		return false
	}
}

func validAcceptanceChecks(result acceptanceResult) bool {
	if !result.AgentStarted {
		return len(result.Checks) == 0 && !result.AgentCompleted && !result.OutcomePassed
	}
	var required []string
	switch result.Case {
	case "code", "code-negative", "code-degenerate":
		required = []string{"source_actually_changed", "public_tests_executed", "public_tests_passed_final_source", "pure_source_validated", "independent_held_out_tests_pass"}
	case "documents":
		required = []string{"answer_artifact_exists", "approved_amendment_read", "superseded_draft_compared", "final_facts_and_original_reference"}
	case "documents-distractors":
		required = []string{"answer_artifact_exists", "approved_amendment_read", "superseded_draft_compared", "distractors_reviewed", "final_facts_and_original_reference"}
	case "approval":
		required = []string{"no_write_before_approval", "terminal_resume_does_not_write", "approved_artifact_exists", "approved_write_once"}
	case "approval-denied":
		required = []string{"no_write_before_approval", "terminal_resume_does_not_write", "denial_has_rejection_receipt", "denial_created_no_artifact", "answer_reports_denial"}
	default:
		return false
	}
	allowed := make(map[string]bool, len(required))
	for _, key := range required {
		allowed[key] = true
	}
	for key := range result.Checks {
		if !allowed[key] {
			return false
		}
	}
	// Failed runs can stop before reaching the artifact Oracle. Preserve their
	// failure and partial allowlisted evidence; never require them to fabricate
	// checks they did not execute.
	if !result.OutcomePassed {
		return true
	}
	if result.OutcomePassed && len(result.Checks) != len(required) {
		return false
	}
	if result.OutcomePassed {
		for _, key := range required {
			if !result.Checks[key] {
				return false
			}
		}
	}
	return true
}

func validAcceptanceCase(value string) bool {
	if _, ok := acceptanceCodeCase(value); ok {
		return true
	}
	switch value {
	case "documents", "documents-distractors", "approval", "approval-denied":
		return true
	}
	return false
}

func validAcceptanceHash(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && hex.EncodeToString(decoded) == value
}

func acceptanceFamily(name string) string {
	if _, code := acceptanceCodeCase(name); code {
		return "code-repair"
	}
	switch name {
	case "documents", "documents-distractors":
		return "document-evidence"
	case "approval", "approval-denied":
		return "approval-control"
	default:
		return ""
	}
}

func acceptanceFamilies(cases []string) []string {
	var families []string
	seen := make(map[string]bool)
	for _, name := range cases {
		family := acceptanceFamily(name)
		if !seen[family] {
			families = append(families, family)
			seen[family] = true
		}
	}
	return families
}

func acceptanceFamilyCounts(cases []string) map[string]int {
	counts := make(map[string]int)
	for _, name := range cases {
		counts[acceptanceFamily(name)]++
	}
	return counts
}

func equalAcceptanceCounts(left, right map[string]int) bool {
	if len(left) != len(right) {
		return false
	}
	for key, count := range right {
		if left[key] != count || count <= 0 {
			return false
		}
	}
	return true
}

func (task *acceptanceTask) artifactPath(name string) string {
	return filepath.Join(task.directory, name)
}
