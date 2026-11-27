//go:build !cognito_hosted

package cognitoidp

import "errors"

func NewHostedBrowser(_ *Store, _, _ string) (HostedBrowser, error) {
	return nil, errors.New("Cognito hosted login requires the explicit cognito_hosted test build")
}
