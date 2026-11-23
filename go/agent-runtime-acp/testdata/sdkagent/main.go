package main

import (
	"context"
	"errors"
	"log"
	"os"

	wire "github.com/caelis-labs/acp-go-sdk"
	"github.com/caelis-labs/acp-go-sdk/transport/stdio"
)

type agent struct{ conn *wire.AgentSideConnection }

func (*agent) Initialize(context.Context, wire.InitializeRequest) (wire.InitializeResponse, error) {
	return wire.InitializeResponse{ProtocolVersion: wire.ProtocolVersion(wire.WireProtocolVersion),
		AgentInfo: &wire.Implementation{Name: "gaoge-test-agent", Version: "1.0"}, AuthMethods: []wire.AuthMethod{}}, nil
}
func (*agent) NewSession(context.Context, wire.NewSessionRequest) (wire.NewSessionResponse, error) {
	return wire.NewSessionResponse{SessionId: wire.SessionId("sdk-session")}, nil
}
func (a *agent) Prompt(ctx context.Context, req wire.PromptRequest) (wire.PromptResponse, error) {
	session := req.SessionId
	if len(req.Prompt) == 1 && req.Prompt[0].Text != nil && req.Prompt[0].Text.Text == "wrong-session" {
		session = wire.SessionId("forged-session")
	}
	if err := a.conn.SessionUpdate(ctx, wire.SessionNotification{SessionId: session,
		Update: wire.UpdateAgentMessageText("SDK ")}); err != nil {
		return wire.PromptResponse{}, err
	}
	if err := a.conn.SessionUpdate(ctx, wire.SessionNotification{SessionId: session,
		Update: wire.UpdateAgentMessageText("response")}); err != nil {
		return wire.PromptResponse{}, err
	}
	return wire.PromptResponse{StopReason: wire.StopReasonEndTurn}, nil
}
func (*agent) Cancel(context.Context, wire.CancelNotification) error { return nil }
func main() {
	a := &agent{}
	conn, err := stdio.NewAgentConnection(a, wire.ConnectionOptions{})
	if err != nil {
		log.Print(err)
		os.Exit(1)
	}
	a.conn = conn
	defer conn.Close()
	if err = conn.Wait(context.Background()); err != nil && !errors.Is(err, wire.ErrPeerClosed) {
		log.Print(err)
	}
}

var _ wire.Agent = (*agent)(nil)
