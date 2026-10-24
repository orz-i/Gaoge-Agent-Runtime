// task-agent runs a synthetic evidence-to-draft task and resumes it after a
// committed Tool receipt. The default scripted model needs no network or keys.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/model"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/provider"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/provider/anthropic"
	"github.com/orz-i/Gaoge-Agent-Runtime/go/agent-runtime/provider/openai"
)

const serviceInputTokens int64 = 6144

type settings struct {
	mode, provider, modelID     string
	contextTokens, outputTokens int64
}

func main() {
	var config settings
	flag.StringVar(&config.mode, "mode", "scripted", "scripted runtime acceptance, or explicit live model smoke")
	flag.StringVar(&config.provider, "provider", "", "live only: openai or anthropic")
	flag.StringVar(&config.modelID, "model", "", "live only: exact provider model ID")
	flag.Int64Var(&config.contextTokens, "context-tokens", 0, "live only: documented context capacity of the selected model")
	flag.Int64Var(&config.outputTokens, "max-output-tokens", 0, "live only: explicit per-call output ceiling and context reservation")
	flag.Parse()
	if err := config.resolve(); err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	result, err := runTask(ctx, config, config.newModel, defaultSource())
	if err != nil {
		log.Fatal(err)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err = encoder.Encode(result); err != nil {
		log.Fatal(err)
	}
}

func (config *settings) resolve() error {
	switch config.mode {
	case "scripted":
		if config.provider != "" || config.modelID != "" || config.contextTokens != 0 || config.outputTokens != 0 {
			return errors.New("scripted mode has fixed synthetic model limits; provider flags require -mode=live")
		}
		config.modelID, config.contextTokens, config.outputTokens = "scripted-task", 8192, 512
	case "live":
		if (config.provider != "openai" && config.provider != "anthropic") || config.modelID == "" ||
			config.outputTokens <= 0 || config.contextTokens <= config.outputTokens ||
			config.contextTokens-config.outputTokens < serviceInputTokens {
			return errors.New("live mode requires -provider=openai|anthropic, -model, documented -context-tokens and -max-output-tokens, with at least 6144 input tokens remaining")
		}
	default:
		return errors.New("-mode must be scripted or live")
	}
	return nil
}

func (config settings) routedModel() string {
	if config.mode == "scripted" {
		return config.modelID
	}
	return config.provider + "/" + config.modelID
}

// Credentials are read only after explicit live-mode validation. This function
// is never called in live mode by the offline tests or the default command.
func (config settings) newModel() (model.Client, error) {
	if config.mode == "scripted" {
		return scriptedModel{}, nil
	}
	var client model.Client
	var err error
	switch config.provider {
	case "openai":
		client, err = openai.New(openai.Config{APIKey: os.Getenv("OPENAI_API_KEY")})
	case "anthropic":
		client, err = anthropic.New(anthropic.Config{APIKey: os.Getenv("ANTHROPIC_API_KEY")})
	default:
		return nil, fmt.Errorf("unsupported provider %q", config.provider)
	}
	if err != nil {
		return nil, err
	}
	return provider.NewRouter(config.provider, provider.Binding{Name: config.provider, Client: client})
}
