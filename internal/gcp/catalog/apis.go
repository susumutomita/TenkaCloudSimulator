package catalog

// APIs returns every reviewed bounded Google API specification.
func APIs() []*API {
	return []*API{alloyDB, artifactRegistry, cloudBuild, cloudDeploy, workstations, developerConnect, secureSourceManager, appHosting,
		blockchainNodeEngine, clusterDirector, vmwareEngine, osConfig, workloadManager}
}
