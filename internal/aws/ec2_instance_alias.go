package aws

// EC2InstancePublicDNSName is a local simulator projection, not public DNS or
// routing authority. The Host bridge resolves it only through an actual retained
// receipt; consumers must never DNS-dial it or fall back to an external fetch.
func EC2InstancePublicDNSName(instance string) string {
	return instance + ".simulator.invalid"
}
