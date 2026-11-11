package main

import (
	"errors"
	"flag"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"github.com/susumutomita/TenkaCloudSimulator/internal/inference"
)

const (
	defaultBedrockTimeout     = time.Minute
	defaultBedrockConcurrency = 2
)

// bedrockCLIFlags holds parsed values only. It contacts no backend, opens no
// listener and reads no environment variable; run validates before startup.
type bedrockCLIFlags struct {
	backend, ollamaURL                                        string
	models                                                    []string
	timeout                                                   time.Duration
	concurrency                                               int
	backendSet, urlSet, modelsSet, timeoutSet, concurrencySet bool
}

// registerBedrockFlags adds the opt-in Bedrock Runtime flags. Every flag
// records that it was supplied, so disabled mode cannot silently ignore one.
func registerBedrockFlags(set *flag.FlagSet) (*bedrockCLIFlags, error) {
	if set == nil {
		return nil, errors.New("Bedrock Runtime flag registration requires a flag set")
	}
	values := &bedrockCLIFlags{timeout: defaultBedrockTimeout, concurrency: defaultBedrockConcurrency}
	set.Func("bedrock-runtime-backend", "Enable Bedrock Runtime InvokeModel/Converse with an explicit local backend: ollama or apple (no cloud fallback)", func(value string) error {
		values.backend, values.backendSet = value, true
		return nil
	})
	set.Func("bedrock-ollama-url", "Numeric-loopback Ollama URL (default "+inference.DefaultOllamaURL+"; no proxy, redirect or remote host)", func(value string) error {
		values.ollamaURL, values.urlSet = value, true
		return nil
	})
	set.Func("bedrock-model", "Repeatable operator mapping MODEL_ID=LOCAL_MODEL, for example anthropic.claude-3-haiku-20240307-v1:0=llama3.2:1b (models are never downloaded)", func(value string) error {
		values.models, values.modelsSet = append(values.models, value), true
		return nil
	})
	set.Func("bedrock-runtime-timeout", "Per-request local generation deadline, "+inference.MinTimeout.String()+"–"+inference.MaxTimeout.String()+" (default "+defaultBedrockTimeout.String()+")", func(value string) error {
		parsed, err := time.ParseDuration(value)
		if err != nil {
			return errors.New("use a Go duration such as 90s")
		}
		values.timeout, values.timeoutSet = parsed, true
		return nil
	})
	set.Func("bedrock-runtime-concurrency", fmt.Sprintf("Simultaneous local generations admitted, %d–%d (default %d); excess requests are throttled", inference.MinConcurrency, inference.MaxConcurrency, defaultBedrockConcurrency), func(value string) error {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return errors.New("use an integer")
		}
		values.concurrency, values.concurrencySet = parsed, true
		return nil
	})
	return values, nil
}

// validateBedrockFlags builds the trusted runtime or explains why it cannot.
// A selected backend that is unavailable fails startup; nothing falls back.
func validateBedrockFlags(values bedrockCLIFlags, unsafeNoAuth bool) (*aws.BedrockRuntime, error) {
	if !values.backendSet {
		if values.urlSet || values.modelsSet || values.timeoutSet || values.concurrencySet {
			return nil, errors.New("Bedrock Runtime flags require --bedrock-runtime-backend")
		}
		return nil, nil
	}
	if unsafeNoAuth {
		return nil, errors.New("--bedrock-runtime-backend requires authenticated access; cannot combine with --unsafe-no-auth")
	}
	var backend inference.Backend
	switch values.backend {
	case "ollama":
		raw := values.ollamaURL
		if !values.urlSet {
			raw = inference.DefaultOllamaURL
		}
		ollama, err := inference.NewOllama(raw)
		if err != nil {
			return nil, fmt.Errorf("--bedrock-ollama-url: %w", err)
		}
		backend = ollama
	case "apple":
		if values.urlSet {
			return nil, errors.New("--bedrock-ollama-url applies only to --bedrock-runtime-backend ollama")
		}
		apple, err := inference.AppleFoundationModels()
		if err != nil {
			return nil, fmt.Errorf("--bedrock-runtime-backend apple: %w", err)
		}
		backend = apple
	default:
		return nil, errors.New("--bedrock-runtime-backend must be ollama or apple")
	}
	if len(values.models) == 0 {
		return nil, errors.New("--bedrock-runtime-backend requires at least one --bedrock-model MODEL_ID=LOCAL_MODEL mapping")
	}
	models := make(map[string]string, len(values.models))
	for _, mapping := range values.models {
		id, local, ok := strings.Cut(mapping, "=")
		if !ok || id == "" || local == "" {
			return nil, errors.New("--bedrock-model must be MODEL_ID=LOCAL_MODEL")
		}
		if _, duplicate := models[id]; duplicate {
			return nil, fmt.Errorf("--bedrock-model maps %s more than once", id)
		}
		models[id] = local
	}
	return aws.NewBedrockRuntime(aws.BedrockRuntimeConfig{Backend: backend, Models: models, Timeout: values.timeout, Concurrency: values.concurrency})
}
