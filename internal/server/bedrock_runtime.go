package server

import "github.com/susumutomita/TenkaCloudSimulator/internal/aws"

// bedrockRuntimeCapabilities reports the opt-in local inference contract.
// It names mapped Bedrock model IDs and the backend kind, never the backend
// address, backend model names, prompts or responses.
func bedrockRuntimeCapabilities(runtime *aws.BedrockRuntime) map[string]any {
	capabilities := map[string]any{
		"enabled":                  runtime != nil,
		"protocol":                 "REST/JSON 2023-09-30, signing name bedrock",
		"operations":               []string{"InvokeModel", "Converse"},
		"iamAction":                "bedrock:InvokeModel on arn:aws:bedrock:{region}::foundation-model/{modelId}",
		"invokeModelBody":          "Anthropic Claude Messages (anthropic_version bedrock-2023-05-31), text only",
		"content":                  "text only",
		"streaming":                false,
		"tools":                    false,
		"fallback":                 "none: disabled, unmapped or unavailable backends return errors",
		"modelDownload":            false,
		"persistence":              "none: prompts and responses are not stored or logged",
		"requestBytes":             aws.BedrockRuntimeRequestBytes,
		"maxOutputTokens":          aws.BedrockMaxOutputTokens,
		"defaultConverseMaxTokens": aws.BedrockDefaultMaxTokens,
		"console":                  false,
		"apiReference":             false,
		"unsupported": []string{"InvokeModelWithResponseStream", "ConverseStream", "InvokeModelWithBidirectionalStream", "CountTokens", "ApplyGuardrail", "async invocation",
			"images, documents, video, tool use, reasoning, citations and cache points", "stop sequences", "assistant prefill", "guardrails, trace, latency and service-tier options",
			"inference profile and non-foundation-model ARNs", "Apple Foundation Models backend in this build", "Claude Code compatibility"},
	}
	if runtime != nil {
		capabilities["backend"] = runtime.BackendName()
		capabilities["models"] = runtime.ModelIDs()
		capabilities["timeoutSeconds"] = int(runtime.Timeout().Seconds())
		capabilities["concurrency"] = runtime.Concurrency()
	}
	return capabilities
}
