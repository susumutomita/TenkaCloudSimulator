package catalog

// APIs returns every reviewed bounded Google API specification.
func APIs() []*API {
	return []*API{alloyDB, serviceDirectory, networkConnectivity, networkManagement, cloudIDS, networkSecurity, networkServices, cloudLogging, cloudMonitoring}
}
