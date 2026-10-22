package agent

import (
	"context"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/kernel"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

// Keep a paused assistant turn in the durable transcript, including an empty
// content placeholder for the host's opaque provider replay. The next model
// invocation has its own identity and consumes the ordinary call/token budget.
func (runner *Runner) continueModel(ctx context.Context, snapshot kernel.Snapshot, state runState, response model.Response) (kernel.Snapshot, error) {
	state.Messages = append(state.Messages, model.Message{Role: model.RoleAssistant, Content: response.Content, ProviderReplay: model.CloneProviderReplay(response.ProviderReplay)})
	encoded, err := encodeState(state)
	if err != nil {
		return kernel.Snapshot{}, err
	}
	return runner.runtime.Apply(ctx, snapshot.Run.ID, snapshot.Run.Revision, kernel.Mutation{
		Status: kernel.RunStatusRunning, State: encoded, Checkpoint: snapshot.Checkpoint,
		Events: []kernel.EventDraft{{Type: "agent.model_continued", Message: response.ResponseID, Wakeup: true,
			WakeupAt: agentWakeupAt(runner.clock.Now())}},
	})
}
