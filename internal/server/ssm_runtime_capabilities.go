package server

func nativeCommandCapabilities(enabled bool) map[string]any {
	operations := []string{}
	if enabled {
		operations = []string{"SendCommand", "GetCommandInvocation", "ListCommands"}
	}
	return map[string]any{
		"enabled": enabled, "operations": operations,
		"document":          "AWS-RunShellScript only; stored custom documents are not executed",
		"targetsPerCommand": 8, "sameDeploymentOnly": true, "retainedCommands": 128,
		"listFilter":          "exactly one InvokedAfter; MaxResults 1–50",
		"visibility":          "same role incarnation and region; narrower than AWS account-wide listing",
		"authentication":      "verified temporary assumed-role signature and current IAM policy",
		"uncertainSubmission": "discover original command; never automatically resubmit",
		"physicalReadiness":   "configuration is not evidence of guest execution",
	}
}

func nativeSessionCapabilities(enabled bool) map[string]any {
	operations := []string{}
	if enabled {
		operations = []string{"StartSession", "ResumeSession", "TerminateSession"}
	}
	return map[string]any{
		"enabled": enabled, "operations": operations,
		"transport":         "bounded local terminal WebSocket; not AWS session-manager-plugin compatible",
		"authentication":    "verified temporary role and target-bound one-use terminal handoff",
		"physicalReadiness": "configured image pin does not establish a live guest relay",
	}
}
