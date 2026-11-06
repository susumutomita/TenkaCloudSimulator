package catalog

// Services returns every reviewed bounded catalog specification.
func Services() []*Service {
	return []*Service{
		// Developer tools and application services.
		amplify, codeConnections, ecr, stepFunctions,
		// Analytics.
		athena, dataExchange, entityResolution, glue, kinesis, openSearch,
		// Application integration and messaging.
		appFabric, eventBridge, managedBlockchain, mwaa, pinpoint, sesV2, smsVoice,
		// Compute, containers and developer tools.
		appRunner, codeArtifact, codeBuild, codeCommit, codeDeploy, codePipeline, ecs, eks, imageBuilder,
		// Management and governance.
		appConfig, awsConfig, cloudTrail, dataLifecycleManager, managedPrometheus, resourceGroups, serviceCatalog,
		// Security, identity and compliance.
		acm, acmPCA, detective, ram, securityHub, shield, signer, verifiedPermissions,
	}
}
