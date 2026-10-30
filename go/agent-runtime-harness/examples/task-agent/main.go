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
	mode, provider, modelID     string
	contextTokens, outputTokens int64
	endpoint, apiKey, sampleID  string
	allowUnknownStructuredModel bool
	httpClient                  *http.Client
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
	flags.StringVar(&config.mode, "mode", "scripted", "scripted, live, or baseline-stdin")
	flags.StringVar(&config.provider, "provider", "", "live only: openai or anthropic")
	flags.StringVar(&config.modelID, "model", "", "live only: exact provider model ID")
	flags.Int64Var(&config.contextTokens, "context-tokens", 0, "live only: documented model capacity")
	flags.Int64Var(&config.outputTokens, "max-output-tokens", 0, "live only: output ceiling")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return configurationError(output)
	}
	if config.mode == "baseline-stdin" {
		if config.provider != "" || config.modelID != "" || config.contextTokens != 0 || config.outputTokens != 0 {
			return configurationError(output)
		}
		return runBaseline(ctx, input, output)
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
	var allowed []string
	if config.allowUnknownStructuredModel {
		allowed = []string{config.modelID}
	}
	var client model.Client
	var err error
	switch config.provider {
	case "openai":
		client, err = openai.New(openai.Config{APIKey: key, Endpoint: config.endpoint, HTTPClient: config.httpClient, StructuredOutputModels: allowed})
	case "anthropic":
		client, err = anthropic.New(anthropic.Config{APIKey: key, Endpoint: config.endpoint, HTTPClient: config.httpClient, StructuredOutputModels: allowed})
	default:
		return nil, errors.New("unsupported provider")
	}
	if err != nil {
		return nil, err
	}
	return provider.NewRouter(config.provider, provider.Binding{Name: config.provider, Client: client})
}
