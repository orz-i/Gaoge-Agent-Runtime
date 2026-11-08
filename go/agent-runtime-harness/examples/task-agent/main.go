// task-agent provides an offline acceptance demo and an explicit live baseline.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	provider "github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/anthropic"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime-provider/openai"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
)

const serviceInputTokens int64 = 8192

type settings struct {
	mode, provider, modelID                  string
	contextTokens, outputTokens              int64
	endpoint, apiKey, sampleID, bindingScope string
	allowUnknownStructuredModel              bool
	reasoningEffort                          string
	suite                                    string // Validated Host baseline suite, not Model policy.
	modelProfiles                            model.ModelProfileSource
	httpClient                               *http.Client
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := runCommand(ctx, os.Args[1:], os.Stdin, os.Stdout)
	stop()
	os.Exit(code)
}

func runCommand(ctx context.Context, args []string, input io.Reader, output io.Writer) int {
	var config settings
	flags := flag.NewFlagSet("task-agent", flag.ContinueOnError)
	// Flag errors may quote arbitrary argv. Emit only a bounded classification.
	flags.SetOutput(io.Discard)
	flags.StringVar(&config.mode, "mode", "scripted", "scripted, quality-scripted, live, baseline-stdin, baseline-preflight-stdin, baseline-pilot-stdin, or baseline-distractor-pilot-stdin")
	flags.StringVar(&config.provider, "provider", "", "live only: openai or anthropic")
	flags.StringVar(&config.modelID, "model", "", "live only: exact provider model ID")
	flags.Int64Var(&config.contextTokens, "context-tokens", 0, "live only: documented model capacity")
	flags.Int64Var(&config.outputTokens, "max-output-tokens", 0, "live only: output ceiling")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return configurationError(output)
	}
	if config.mode == "baseline-stdin" || config.mode == "baseline-preflight-stdin" || config.mode == "baseline-pilot-stdin" || config.mode == "baseline-distractor-pilot-stdin" {
		if config.provider != "" || config.modelID != "" || config.contextTokens != 0 || config.outputTokens != 0 {
			return configurationError(output)
		}
		switch config.mode {
		case "baseline-preflight-stdin":
			return runBaselinePreflightOnly(ctx, input, output)
		case "baseline-pilot-stdin":
			return runBaselinePilot(ctx, input, output)
		case "baseline-distractor-pilot-stdin":
			return runBaselineDistractorPilot(ctx, input, output)
		default:
			return runBaseline(ctx, input, output)
		}
	}
	if config.mode == "quality-scripted" {
		if config.provider != "" || config.modelID != "" || config.contextTokens != 0 || config.outputTokens != 0 {
			return configurationError(output)
		}
		return runScriptedQuality(ctx, output)
	}
	if err := config.resolve(); err != nil {
		return configurationError(output)
	}
	runCtx, cancel := context.WithTimeout(ctx, sampleTimeout)
	defer cancel()
	result, err := runTask(runCtx, config, config.newModel, defaultSource())
	encoder := json.NewEncoder(output)
	if err != nil {
		result.Answer = taskAnswer{} // Never print an unverified model answer.
	}
	if encoder.Encode(result) != nil {
		return 1
	}
	if err != nil {
		return 1
	}
	return 0
}

func (config *settings) resolve() error {
	switch config.mode {
	case "scripted":
		if config.provider != "" || config.modelID != "" || config.contextTokens != 0 || config.outputTokens != 0 {
			return errors.New("scripted mode requires its fixed synthetic model limits")
		}
		config.modelID, config.contextTokens, config.outputTokens = "scripted-task", 16384, 512
	case "live":
		if (config.provider != "openai" && config.provider != "anthropic") || !validModelID(config.modelID) ||
			config.outputTokens <= 0 || config.contextTokens <= config.outputTokens ||
			config.contextTokens-config.outputTokens < serviceInputTokens {
			return errors.New("live mode requires explicit provider, model and sufficient capacity")
		}
	default:
		return errors.New("invalid mode")
	}
	return nil
}

// Model options are frozen through ConfigSnapshot -> ModelInvocation, not
// injected by middleware or changed on recovery. Only a validated effort
// string reaches this helper in the interactive baseline path.
func (config settings) modelOptions() json.RawMessage {
	if config.reasoningEffort == "" {
		return nil
	}
	encoded, _ := json.Marshal(map[string]any{
		"reasoning": map[string]string{"effort": config.reasoningEffort},
	})
	return encoded
}

func (config settings) routedModel() string {
	if config.mode == "scripted" {
		return config.modelID
	}
	return config.provider + "/" + config.modelID
}

// baseline-stdin supplies credentials only in memory. Environment lookup is
// retained exclusively for the original explicit live-mode command.
func (config settings) newModel() (model.Client, error) {
	if config.mode == "scripted" {
		return scriptedModel{}, nil
	}
	key := config.apiKey
	if key == "" {
		switch config.provider {
		case "openai":
			key = os.Getenv("OPENAI_API_KEY")
		case "anthropic":
			key = os.Getenv("ANTHROPIC_API_KEY")
		}
	}
	profiles, err := config.hostModelProfileSource()
	if err != nil {
		return nil, err
	}
	var client model.Client
	switch config.provider {
	case "openai":
		client, err = openai.New(openai.Config{APIKey: key, Endpoint: config.endpoint, HTTPClient: config.httpClient,
			BindingScope: config.bindingScope, ModelProfiles: profiles})
	case "anthropic":
		client, err = anthropic.New(anthropic.Config{APIKey: key, Endpoint: config.endpoint, HTTPClient: config.httpClient,
			BindingScope: config.bindingScope, ModelProfiles: profiles})
	default:
		return nil, errors.New("unsupported provider")
	}
	if err != nil {
		return nil, err
	}
	return provider.NewRouter(config.provider, provider.Binding{Name: config.provider, Identity: "host-binding-v1", Client: client})
}
