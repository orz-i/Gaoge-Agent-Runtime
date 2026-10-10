package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// AutoAllowancePolicy opts one direct Agent Run into bounded renewal at the
// P1 safe dispatch boundary. It never raises the frozen hard Budget limits.
// Omit the policy to preserve the exact P1 manual continuation behavior.
type AutoAllowancePolicy struct {
	ModelStep           int `json:"modelStep,omitempty"`
	ToolStep            int `json:"toolStep,omitempty"`
	MaxModelRenewals    int `json:"maxModelRenewals,omitempty"`
	MaxToolRenewals     int `json:"maxToolRenewals,omitempty"`
	MaxNoProgressWindow int `json:"maxNoProgressWindow"`
}

func autoAllowanceProgress(state *autoAllowanceState) *AutoAllowanceProgress {
	if state == nil {
		return nil
	}
	progress := state.AutoAllowanceProgress
	return &progress
}

// AutoAllowanceProgress contains only counts, not work/output fingerprints.
type AutoAllowanceProgress struct {
	ModelRenewals int `json:"modelRenewals"`
	ToolRenewals  int `json:"toolRenewals"`
	ModelStalls   int `json:"modelStalls"`
	ToolStalls    int `json:"toolStalls"`
}

type autoAllowanceState struct {
	Policy AutoAllowancePolicy `json:"policy"`
	AutoAllowanceProgress
	ModelFingerprint string `json:"modelFingerprint,omitempty"`
	ToolFingerprint  string `json:"toolFingerprint,omitempty"`
}

const maxAutomaticRenewalsPerDimension = 16
const maxAutoNoProgressWindow = 4

func initialAutoAllowance(request *AutoAllowancePolicy, allowance *CallAllowance, hard Limits) (*autoAllowanceState, error) {
	if request == nil {
		return nil, nil
	}
	if !validAutoAllowancePolicy(*request, allowance, hard) {
		return nil, ErrInvalidCallAllowance
	}
	return &autoAllowanceState{Policy: *request}, nil
}

func validAutoAllowancePolicy(policy AutoAllowancePolicy, allowance *CallAllowance, hard Limits) bool {
	if allowance == nil || policy.MaxNoProgressWindow < 1 || policy.MaxNoProgressWindow > maxAutoNoProgressWindow {
		return false
	}
	modelOn := policy.ModelStep > 0 && policy.MaxModelRenewals > 0 && allowance.LLMCalls > 0
	toolOn := policy.ToolStep > 0 && policy.MaxToolRenewals > 0 && allowance.ToolCalls > 0
	if !modelOn && !toolOn {
		return false
	}
	if policy.ModelStep < 0 || policy.ToolStep < 0 ||
		policy.MaxModelRenewals < 0 || policy.MaxToolRenewals < 0 ||
		policy.MaxModelRenewals > maxAutomaticRenewalsPerDimension ||
		policy.MaxToolRenewals > maxAutomaticRenewalsPerDimension ||
		policy.ModelStep > hard.MaxLLMCalls || policy.ToolStep > hard.MaxToolCalls {
		return false
	}
	if (policy.ModelStep == 0) != (policy.MaxModelRenewals == 0) ||
		(policy.ToolStep == 0) != (policy.MaxToolRenewals == 0) {
		return false
	}
	return true
}

func validAutoAllowanceState(state *autoAllowanceState, allowance *CallAllowance, hard Limits) bool {
	if state == nil {
		return true
	}
	if !validAutoAllowancePolicy(state.Policy, allowance, hard) {
		return false
	}
	if state.ModelRenewals < 0 || state.ModelRenewals > state.Policy.MaxModelRenewals ||
		state.ToolRenewals < 0 || state.ToolRenewals > state.Policy.MaxToolRenewals ||
		state.ModelStalls < 0 || state.ModelStalls > state.Policy.MaxNoProgressWindow ||
		state.ToolStalls < 0 || state.ToolStalls > state.Policy.MaxNoProgressWindow {
		return false
	}
	for _, hash := range []string{state.ModelFingerprint, state.ToolFingerprint} {
		if hash == "" {
			continue
		}
		if len(hash) != 64 {
			return false
		}
		if _, err := hex.DecodeString(hash); err != nil {
			return false
		}
	}
	return true
}

// advanceCallAllowance runs *before* external model/Tool admission. When an
// opt-in renewal is allowed it atomically keeps the same Run running and
// publishes a durable delayed wakeup; otherwise P1 manual pause still applies.
func (runner *Runner) advanceCallAllowance(
	ctx context.Context, snapshot kernel.Snapshot, state runState, dimension string,
) (kernel.Snapshot, bool, error) {
	if next, renewed, err := runner.autoRenewCallAllowance(ctx, snapshot, state, dimension); renewed || err != nil {
		// Hosted workers must yield at each durable segment handoff so a
		// request does not hold the whole long-running loop. The scheduler
		// resumes from this same CAS revision and the stored wakeup.
		return next, !renewed || runner.deferResume, err
	}
	paused, err := runner.pauseCallAllowance(ctx, snapshot, state, dimension)
	return paused, true, err
}

func (runner *Runner) autoRenewCallAllowance(
	ctx context.Context, snapshot kernel.Snapshot, state runState, dimension string,
) (kernel.Snapshot, bool, error) {
	auto := state.AutoAllowance
	if auto == nil || state.CallAllowance == nil {
		return kernel.Snapshot{}, false, nil
	}
	if ctx.Err() != nil {
		return snapshot, false, ctx.Err()
	}
	if snapshot.Run.Status != kernel.RunStatusRunning {
		return kernel.Snapshot{}, false, nil
	}
	if snapshot.Run.DeadlineAt != nil && !snapshot.Run.DeadlineAt.After(runner.clock.Now()) {
		return kernel.Snapshot{}, false, nil
	}
	var step, maxRenewals, usedRenewals, available int
	var previousHash string
	var priorStalls int
	switch dimension {
	case "model":
		step, maxRenewals, usedRenewals = auto.Policy.ModelStep, auto.Policy.MaxModelRenewals, auto.ModelRenewals
		available = state.Budget.Limits.MaxLLMCalls - state.CallAllowance.LLMCalls
		previousHash, priorStalls = auto.ModelFingerprint, auto.ModelStalls
	case "tool":
		step, maxRenewals, usedRenewals = auto.Policy.ToolStep, auto.Policy.MaxToolRenewals, auto.ToolRenewals
		available = state.Budget.Limits.MaxToolCalls - state.CallAllowance.ToolCalls
		previousHash, priorStalls = auto.ToolFingerprint, auto.ToolStalls
	default:
		return kernel.Snapshot{}, false, ErrInvalidCallAllowance
	}
	if step <= 0 || usedRenewals >= maxRenewals || available <= 0 {
		return kernel.Snapshot{}, false, nil
	}
	fingerprint := lastSettledWorkFingerprint(state.Messages)
	stalls := 0
	if previousHash != "" && previousHash == fingerprint {
		stalls = priorStalls + 1
	}
	// Comparing identical *settled* outcomes is conservative; an ambiguous
	// write is never auto retried and a stall falls back to manual approval.
	if stalls >= auto.Policy.MaxNoProgressWindow {
		return kernel.Snapshot{}, false, nil
	}
	if step > available {
		step = available
	}
	updated := *auto
	next := *state.CallAllowance
	if dimension == "model" {
		next.LLMCalls += step
		updated.ModelRenewals++
		updated.ModelStalls = stalls
		updated.ModelFingerprint = fingerprint
	} else {
		next.ToolCalls += step
		updated.ToolRenewals++
		updated.ToolStalls = stalls
		updated.ToolFingerprint = fingerprint
	}
	state.CallAllowance = &next
	state.AutoAllowance = &updated
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, false, err
	}
	renewed, err := runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{
			Type: "agent.allowance_auto_granted", Message: dimension, Wakeup: true,
			WakeupAt: agentWakeupAt(runner.clock.Now()),
		}},
	})
	if err != nil {
		return renewed, false, err
	}
	// Only one CAS winner may renew; after a crash the durable wakeup resumes
	// from this updated state, never from an old pre-grant checkpoint.
	return renewed, true, nil
}

// lastSettledWorkFingerprint is a deliberately conservative no-progress
// heuristic. Ignore volatile Tool IDs; hash normalized outcome and Tool args,
// never emit body text or the hash in publicly observable events or logs.
func lastSettledWorkFingerprint(messages []model.Message) string {
	for i := len(messages) - 1; i >= 0; i-- {
		message := messages[i]
		if message.Role == model.RoleTool {
			identity := struct {
				Key     string `json:"key"`
				Args    string `json:"args,omitempty"`
				Content string `json:"content"`
			}{Content: message.Content}
			for j := i - 1; j >= 0; j-- {
				if messages[j].Role != model.RoleAssistant {
					continue
				}
				for _, call := range messages[j].ToolCalls {
					if call.ID == message.ToolCallID {
						identity.Key = call.ToolKey
						identity.Args = canonicalToolArguments(call.Arguments)
						break
					}
				}
				if identity.Key != "" {
					break
				}
			}
			raw, _ := json.Marshal(identity)
			sum := sha256.Sum256(raw)
			return hex.EncodeToString(sum[:])
		}
		if message.Role == model.RoleAssistant && (strings.TrimSpace(message.Content) != "" || len(message.ToolCalls) != 0) {
			value := struct {
				Content string `json:"content"`
				Calls   []struct {
					Key  string `json:"key"`
					Args string `json:"args,omitempty"`
				} `json:"calls,omitempty"`
			}{Content: message.Content}
			for _, call := range message.ToolCalls {
				value.Calls = append(value.Calls, struct {
					Key  string `json:"key"`
					Args string `json:"args,omitempty"`
				}{Key: call.ToolKey, Args: canonicalToolArguments(call.Arguments)})
			}
			raw, _ := json.Marshal(value)
			sum := sha256.Sum256(raw)
			return hex.EncodeToString(sum[:])
		}
	}
	// An unchanged empty transcript cannot certify progress; a fixed
	// fingerprint means successive auto renewals eventually pause.
	sum := sha256.Sum256([]byte("no-settled-work"))
	return hex.EncodeToString(sum[:])
}

func canonicalToolArguments(arguments json.RawMessage) string {
	if len(arguments) == 0 {
		return ""
	}
	// Unmarshal's default float64 decoding loses precision above 2^53;
	// a false no-progress match must not collapse distinct Tool parameters.
	if !json.Valid(arguments) {
		return strings.TrimSpace(string(arguments))
	}
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return strings.TrimSpace(string(arguments))
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return strings.TrimSpace(string(arguments))
	}
	return string(encoded)
}

// automaticAllowanceHint only guides model output. It cannot alter Tool
// authorization, Runtime hard limits, actual usage or context ownership.
func automaticAllowanceHint(state runState) string {
	if state.AutoAllowance == nil || state.CallAllowance == nil {
		return ""
	}
	modelLeft := state.CallAllowance.LLMCalls - state.Budget.Usage.LLMCalls
	if modelLeft < 0 || modelLeft > 1 || state.AutoAllowance.Policy.ModelStep <= 0 {
		return ""
	}
	return fmt.Sprintf(
		"Runtime execution window: %d model call(s) remain before a bounded segment boundary. "+
			"Keep progress clear and avoid repeating unchanged Tool work; do not assume new permissions or extra hard-limit capacity.",
		modelLeft,
	)
}
