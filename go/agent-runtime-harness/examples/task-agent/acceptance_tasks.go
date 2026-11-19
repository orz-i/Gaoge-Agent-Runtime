package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
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

const acceptanceVersion = "task-outcome-acceptance-v1"
const acceptanceModelLimit = 8
const acceptanceToolLimit = 8
const acceptanceInputBytes = 64 << 10

type acceptanceResult struct {
	Case                string          `json:"case"`
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
	Version     string             `json:"version"`
	Mode        string             `json:"mode"`
	FixtureOnly bool               `json:"fixtureOnly"`
	Requested   int                `json:"requested"`
	Attempted   int                `json:"attempted"`
	Processed   int                `json:"processed"`
	Provider    string             `json:"provider"`
	Model       string             `json:"model"`
	OutputLimit int64              `json:"maxOutputTokens"`
	ModelLimit  int                `json:"maxModelCalls"`
	ToolLimit   int                `json:"maxToolCalls"`
	InputBytes  int                `json:"maxInputBytes"`
	Context     int64              `json:"contextTokens"`
	Attempts    int                `json:"maxAttemptsPerCall"`
	Results     []acceptanceResult `json:"results"`
}

type acceptanceTask struct {
	name, goal, directory string
	readDocuments         map[string]bool
	approvedWrites        int
	publicTestsRan        bool
	lastTestOutput        string
}

func runAcceptanceCommand(ctx context.Context, config settings, selected string, allowLive bool, output io.Writer) int {
	if selected == "" {
		selected = "all"
	}
	cases := []string{selected}
	if selected == "all" {
		cases = []string{"code", "documents", "approval"}
	}
	for _, name := range cases {
		if name != "code" && name != "documents" && name != "approval" {
			return configurationError(output)
		}
	}
	live := config.provider != ""
	if live {
		if !allowLive {
			return configurationError(output) // no key lookup, model construction or network
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
	report := acceptanceReport{Version: acceptanceVersion, Mode: "fixture", FixtureOnly: !live, Requested: len(cases),
		Provider: "fixture", Model: "acceptance-fixture", OutputLimit: config.outputTokens,
		ModelLimit: acceptanceModelLimit, ToolLimit: acceptanceToolLimit, InputBytes: acceptanceInputBytes, Context: config.contextTokens, Attempts: 1}
	if live {
		report.Mode = "provider_live"
		report.Provider, report.Model = config.provider, config.routedModel()
	}
	exit := 0
	for _, name := range cases {
		var client model.Client = acceptanceFixtureModel{task: name}
		selectedModel := "acceptance-fixture"
		if live {
			var err error
			client, err = config.newModel()
			if err != nil {
				report.Results = append(report.Results, acceptanceResult{Case: name, Failure: "provider_configuration_failed"})
				exit = 1
				break
			}
			selectedModel = config.routedModel()
		}
		taskCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		result := runAcceptanceTask(taskCtx, name, client, selectedModel, config.contextTokens, config.outputTokens)
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
	}
	if json.NewEncoder(output).Encode(report) != nil {
		return 1
	}
	return exit
}

func runAcceptanceTask(ctx context.Context, name string, client model.Client, selectedModel string, contextTokens, outputTokens int64) (result acceptanceResult) {
	started := time.Now()
	defer func() { result.DurationMs = time.Since(started).Milliseconds() }()
	if outputTokens <= 0 {
		outputTokens = 1024
	}
	result = acceptanceResult{Case: name, Checks: make(map[string]bool), ApprovalIsSimulated: name == "approval"}
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
		Version, Name, Goal, Fixtures string
		Tools                         []tools.Definition
		ModelLimit, ToolLimit, Bytes  int
		Attempts                      int
		ContextTokens, OutputTokens   int64
	}{acceptanceVersion, name, task.goal,
		acceptanceBuggyCode + acceptancePublicTests + acceptanceHeldOutTests + acceptanceDocuments + "approved fixture record\n",
		definitions, acceptanceModelLimit, acceptanceToolLimit, acceptanceInputBytes, 1, contextTokens, outputTokens})
	if err != nil {
		result.Failure = "task_identity_unavailable"
		return result
	}
	hash := sha256.Sum256(identity)
	result.TaskHash = hex.EncodeToString(hash[:])
	if name == "code" {
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
			ModelMiddleware: []plugin.ModelMiddleware{acceptanceOutputCeiling(outputTokens)},
		}
		if name == "approval" {
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
	current, runErr := loop.StartRun(ctx, agent.StartRequest{ID: "acceptance-" + name, RequestID: "request-" + name,
		Actor: kernel.ActorRef{TenantID: "fixture", ActorID: "operator"}, Thread: kernel.ThreadRef{Kind: "acceptance", ID: name},
		Model: selectedModel, Goal: task.goal, ToolKeys: keys, RequiredToolKeys: keys})
	result.AgentStarted = current.Run.ID != ""
	if name == "approval" && runErr == nil {
		result.Checks["no_write_before_approval"] = current.Run.Status == kernel.RunStatusWaitingInput && task.approvedWrites == 0
		if result.Checks["no_write_before_approval"] {
			loop, err = compose() // new Runner; original checkpoint/Run is retained
			if err == nil {
				current, runErr = loop.ResolveApproval(ctx, current.Run.ID, current.Run.Revision, plugin.ApprovalResponse{Decision: plugin.ApprovalApprove})
			} else {
				runErr = err
			}
		}
	}
	result.AgentCompleted = runErr == nil && current.Run.Status == kernel.RunStatusCompleted
	if current.Run.ID != "" {
		if view, err := agent.ViewState(current); err == nil {
			result.ModelStepsConsumed = view.Budget.Usage.LLMCalls
			result.ToolCallsConsumed = view.Budget.Usage.ToolCalls
			completeUsage := len(view.ModelInvocations) > 0
			tokens := view.Budget.Usage.TotalTokens
			for _, invocation := range view.ModelInvocations {
				if invocation.Status != agent.ModelInvocationConsumed || invocation.Usage == nil {
					completeUsage = false
					continue
				}
			}
			if completeUsage {
				result.GenerationTokens = &tokens
			}
		}
	}
	if !result.AgentCompleted {
		result.Failure = "agent_did_not_complete"
		return result
	}
	if name == "approval" {
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

type acceptanceOutputCeiling int64

func (acceptanceOutputCeiling) Name() string { return "acceptance.output-ceiling" }
func (ceiling acceptanceOutputCeiling) Model(ctx context.Context, request model.Request, sink model.StreamSink, next plugin.ModelNext) (model.Response, error) {
	if request.MaxOutputTokens == 0 || request.MaxOutputTokens > int64(ceiling) {
		request.MaxOutputTokens = int64(ceiling)
	}
	return next(ctx, request, sink)
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

func (task *acceptanceTask) artifactPath(name string) string {
	return filepath.Join(task.directory, name)
}
