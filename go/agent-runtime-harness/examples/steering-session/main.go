// steering-session is a runnable Host integration, not a real-model quality test.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
	"time"

	harness "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-harness"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/agent"
	runtimecontext "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/context"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/memory"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/tools"
)

const correction = "Look up beta instead"

func main() {
	interactive := flag.Bool("interactive", false, "read the correction while the first Tool is blocked")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := run(ctx, os.Stdin, os.Stdout, *interactive); err != nil {
		fmt.Fprintln(os.Stderr, "steering demo failed:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, input io.Reader, output io.Writer, interactive bool) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	rt, err := kernel.New(kernel.Dependencies{Store: memory.NewStore()})
	if err != nil {
		return err
	}
	queue, err := agent.NewMemoryRunInputs("steering-demo-v1")
	if err != nil {
		return err
	}
	entered, release := make(chan string, 1), make(chan struct{})
	var blocked atomic.Bool
	var history []runtimecontext.Entry
	client := &demoModel{}
	options := harness.AgentSessionOptions{
		Runtime: rt, Store: harness.NewMemoryStore(), Model: client, Inputs: queue,
		Context: harness.AgentSessionContextOptions{
			ResolveWindow: func(context.Context, model.Request) (runtimecontext.ModelWindow, error) {
				return runtimecontext.ModelWindow{ContextTokens: 32768, ReservedOutputTokens: 1024}, nil
			},
		},
		LocalTools: []harness.SessionTool{{
			Registration: tools.Registration{
				Definition: tools.Definition{Key: "lookup", Name: "lookup", Description: "Read a fixture project schedule",
					InputSchema: json.RawMessage(`{"type":"object","properties":{"project":{"type":"string","enum":["alpha","beta"]}},"required":["project"],"additionalProperties":false}`)},
				Handler: tools.HandlerFunc(func(ctx context.Context, request tools.ExecutionRequest) (tools.ExecutionResult, error) {
					if blocked.CompareAndSwap(false, true) {
						entered <- request.RunID
						select {
						case <-release:
						case <-ctx.Done():
							return tools.ExecutionResult{}, ctx.Err()
						}
					}
					var args struct{ Project string }
					if err := json.Unmarshal(request.Call.Arguments, &args); err != nil {
						return tools.ExecutionResult{}, err
					}
					window := map[string]string{"alpha": "09:00", "beta": "18:00"}[args.Project]
					content, _ := json.Marshal(map[string]string{"project": args.Project, "window": window})
					return tools.ExecutionResult{Content: content, Receipt: tools.Receipt{ExecutionID: request.Call.ID, Disposition: "committed"}}, nil
				}),
			},
			Policy: harness.ToolPolicySnapshot{DefinitionVersion: "fixture-v1", ApprovalMode: "never", SideEffectLevel: "read"},
		}},
		Session: harness.SessionOptions{
			HostThread: harness.HostRef{Kind: "conversation", ID: "demo/tenant/chat"},
			Actor:      kernel.ActorRef{TenantID: "demo-tenant", ActorID: "demo-user"},
			Thread:     kernel.ThreadRef{Kind: "conversation", ID: "demo/tenant/chat"},
			Config:     harness.ConfigSnapshot{AgentSpec: harness.AgentSpec{Model: "fixture", MaxModelOutputTokens: 1024}},
			Transcript: harness.TranscriptHistoryFunc(func(context.Context, harness.HostRef, harness.HostRef) ([]runtimecontext.Entry, error) {
				return runtimecontext.CloneEntries(history), nil
			}),
		},
	}
	session, err := harness.OpenAgentSession(options)
	if err != nil {
		return err
	}
	type outcome struct {
		value harness.Snapshot
		err   error
	}
	finished := make(chan outcome, 1)
	go func() {
		value, callErr := session.Prompt(ctx, "turn-1", "Look up alpha")
		finished <- outcome{value, callErr}
	}()
	var runID string
	select {
	case runID = <-entered:
	case result := <-finished:
		return fmt.Errorf("task ended before the input boundary: %w", result.err)
	case <-ctx.Done():
		return ctx.Err()
	}
	if interactive {
		if _, err := fmt.Fprintln(output, "First lookup is running. Type: "+correction); err != nil {
			return err
		}
		line := make(chan string, 1)
		go func() {
			text, _ := bufio.NewReader(io.LimitReader(input, 16<<10)).ReadString('\n')
			line <- strings.TrimSpace(text)
		}()
		select {
		case text := <-line:
			if text != correction {
				close(release)
				return errors.New("this bounded fixture accepts only the displayed correction")
			}
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	current, err := rt.Load(ctx, runID)
	if err != nil {
		return err
	}
	view, err := agent.ViewState(current)
	if err != nil || view.InputBinding != queue.BindingID() || current.Run.Actor != options.Session.Actor || current.Run.Thread != options.Session.Thread {
		return errors.New("host identity or input binding mismatch")
	}
	queued, err := session.Steer(ctx, "turn-1", runID, "input-1", correction)
	if err != nil {
		return err
	}
	if replay, err := session.Steer(ctx, "turn-1", runID, "input-1", correction); err != nil || replay != queued {
		return errors.New("steering replay changed the original queued input")
	}
	if _, err := session.Steer(ctx, "turn-1", runID+"-foreign", "wrong-run", correction); err == nil {
		return errors.New("steering accepted an unrelated Run")
	}
	foreignOptions := options
	foreignOptions.Session.Actor.ActorID = "another-user"
	foreign, err := harness.OpenAgentSession(foreignOptions)
	if err != nil {
		return err
	}
	if _, err := foreign.Steer(ctx, "turn-1", runID, "wrong-user", correction); err == nil {
		return errors.New("steering accepted an unrelated Actor")
	}
	if _, err := fmt.Fprintf(output, "queued=%d applied=%d (queue acceptance is not delivery)\n", queued.Sequence, view.InputCursor); err != nil {
		return err
	}
	close(release)
	var result outcome
	select {
	case result = <-finished:
	case <-ctx.Done():
		return ctx.Err()
	}
	if result.err != nil {
		return result.err
	}
	current, err = rt.Load(ctx, runID)
	if err != nil {
		return err
	}
	view, err = agent.ViewState(current)
	if err != nil || view.InputCursor != queued.Sequence || result.value.Output == nil || string(result.value.Output.Content) != `"beta: 18:00"` {
		return errors.New("the corrected task did not produce the original beta evidence")
	}
	if _, err := fmt.Fprintf(output, "applied=%d answer=beta: 18:00\n", view.InputCursor); err != nil {
		return err
	}
	if _, err = session.Steer(ctx, "turn-1", runID, "late-1", "Look up alpha again"); !errors.Is(err, harness.ErrInvalidRequest) {
		return errors.New("completed task accepted late input")
	}
	// The Host persists all admitted user input, not just the initial prompt.
	for _, entry := range []struct {
		id, text string
		role     model.Role
	}{
		{"turn-1/user", "Look up alpha", model.RoleUser},
		{"turn-1/input-1", correction, model.RoleUser},
		{"turn-1/assistant", "beta: 18:00", model.RoleAssistant},
	} {
		history = append(history, runtimecontext.Entry{ID: entry.id, SourceID: entry.id, TurnID: "turn-1", Message: model.Message{Role: entry.role, Content: model.TextParts(entry.text)}})
	}
	if err := queue.ReleaseRun(ctx, current.Run); err != nil {
		return err
	}
	late, err := session.Prompt(ctx, "turn-2", "Look up alpha again")
	if err != nil || late.Output == nil || string(late.Output.Content) != `"alpha: 09:00"` || late.Turn.ID == result.value.Turn.ID {
		return errors.New("explicit late-input fallback did not complete a distinct Turn")
	}
	if client.duplicateInput {
		return errors.New("host history redelivered the applied correction")
	}
	_, err = fmt.Fprintln(output, "late_input=new_turn answer=alpha: 09:00\nfixture_only=true persistence=memory")
	return err
}

type demoModel struct {
	calls          int
	duplicateInput bool
}

func (client *demoModel) Generate(_ context.Context, request model.Request) (model.Response, error) {
	client.calls++
	project := "alpha"
	corrections := 0
	for _, message := range request.Messages {
		if message.Role == model.RoleUser {
			if model.TextValue(message.Content) == correction {
				corrections++
			}
			if strings.Contains(model.TextValue(message.Content), "beta") {
				project = "beta"
			} else if strings.Contains(model.TextValue(message.Content), "alpha") {
				project = "alpha"
			}
		}
	}
	client.duplicateInput = client.duplicateInput || corrections > 1
	for _, message := range request.Messages {
		var evidence struct{ Project, Window string }
		if message.Role == model.RoleTool && json.Unmarshal([]byte(model.TextValue(message.Content)), &evidence) == nil && evidence.Project == project {
			return model.Response{Content: evidence.Project + ": " + evidence.Window}, nil
		}
	}
	args, _ := json.Marshal(map[string]string{"project": project})
	return model.Response{ToolCalls: []tools.Call{{ID: fmt.Sprintf("lookup-%d", client.calls), ToolKey: "lookup", Arguments: args}}}, nil
}
