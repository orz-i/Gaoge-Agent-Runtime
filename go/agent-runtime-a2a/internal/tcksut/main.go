package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	a2a "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-a2a"
)

const shutdownTimeout = 5 * time.Second

func main() {
	listenAddress := flag.String("listen", "127.0.0.1:9999", "TCP address for the TCK fixture")
	publicURL := flag.String("public-url", "", "public URL advertised by the Agent Card")
	flag.Parse()

	listener, err := net.Listen("tcp", *listenAddress)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	defer func() {
		if closeErr := listener.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
			log.Printf("close listener: %v", closeErr)
		}
	}()

	advertisedURL := *publicURL
	if advertisedURL == "" {
		advertisedURL = "http://" + listener.Addr().String()
	}
	host, err := newTCKHost(advertisedURL)
	if err != nil {
		log.Fatalf("create A2A host: %v", err)
	}

	server := &http.Server{
		Handler:           host.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go shutdownOnSignal(ctx, server)

	fmt.Printf("A2A_TCK_SUT_READY=%s\n", advertisedURL)
	if err = server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("serve: %v", err)
	}
}

func newTCKHost(publicURL string) (*a2a.Host, error) {
	return a2a.NewHost(a2a.HostDependencies{
		Card: a2a.HostedCard{
			PublicURL:          publicURL,
			Name:               "Gaoge Agent Runtime A2A TCK",
			Description:        "Protocol conformance fixture for the product A2A plugin",
			Version:            "0.1.0-beta.4",
			DefaultInputModes:  []string{"text/plain"},
			DefaultOutputModes: []string{"text/plain"},
			Skills: []a2a.RemoteAgentSkill{{
				ID: "tck", Name: "A2A TCK", Description: "Executes official TCK scenarios", Tags: []string{"tck"},
			}},
		},
		Agent:     scenarioAgent{},
		TaskStore: newMemoryTaskStore(),
		Authenticator: a2a.HostedAuthenticatorFunc(func(
			_ context.Context,
			call a2a.HostedCall,
		) (a2a.HostedPrincipal, error) {
			return a2a.HostedPrincipal{Subject: "tck-anonymous", Tenant: call.Tenant}, nil
		}),
		Policy: a2a.HostPolicy{AgentInactivityTimeout: 35 * time.Second, MaxConcurrentExecutions: 64},
	})
}

func shutdownOnSignal(ctx context.Context, server *http.Server) {
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("shutdown: %v", err)
	}
}
