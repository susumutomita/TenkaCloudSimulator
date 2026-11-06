package catalog

// Services returns every reviewed bounded catalog specification.
func Services() []*Service {
	return []*Service{amplify, codeConnections, ecr, stepFunctions}
}
