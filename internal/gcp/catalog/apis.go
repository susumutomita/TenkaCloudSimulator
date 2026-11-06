package catalog

// APIs returns every reviewed bounded Google API specification.
func APIs() []*API {
	return []*API{alloyDB, managedKafka, datastream, dataprocMetastore, dataform, dataFusion, looker, composer, dataplex, bigLake, dataproc}
}
