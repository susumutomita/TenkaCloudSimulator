package catalog

// APIs returns every reviewed bounded Google API specification.
func APIs() []*API {
	return []*API{alloyDB, filestore, netAppVolumes, memorystoreRedis, memorystoreMemcached, backupAndDR, backupForGKE, firestoreAdmin}
}
