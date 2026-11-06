package catalog

// APIs returns every reviewed bounded Google API specification.
func APIs() []*API {
	return []*API{
		alloyDB,
		// Developer tools and compute configuration.
		artifactRegistry, cloudBuild, cloudDeploy, workstations, developerConnect, secureSourceManager, appHosting, blockchainNodeEngine, clusterDirector, vmwareEngine, osConfig, workloadManager,
		// Storage, cache, backup and databases.
		filestore, netAppVolumes, memorystoreRedis, memorystoreMemcached, backupAndDR, backupForGKE, firestoreAdmin, spanner,
		// Data analytics.
		managedKafka, datastream, dataprocMetastore, dataform, dataFusion, looker, composer, dataplex, bigLake, dataproc,
		// Networking, network security and operations.
		serviceDirectory, networkConnectivity, networkManagement, cloudIDS, networkSecurity, networkServices, cloudLogging, cloudMonitoring,
	}
}
