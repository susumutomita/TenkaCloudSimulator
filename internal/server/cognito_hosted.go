package server

import (
	"errors"
	"net/http"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/cognitoidp"
)

const localCognitoAccount = "000000000000"

// LocalCognitoListeners separates the trusted AWS gateway from the Cognito
// browser profile. Wrap AWS in ordinary authenticated NewAccess; configure
// Browser as the sole handler of the dedicated direct loopback TLS listener.
type LocalCognitoListeners struct {
	AWS     http.Handler
	Browser cognitoidp.HostedBrowser
}

// NewWithLocalCognitoHosted composes both handlers with one canonical Cognito
// store. It does not bind ports or alter AWS/console authentication or transport.
func NewWithLocalCognitoHosted(path string, options ProviderConfig, origin string) (LocalCognitoListeners, func() error, error) {
	if !options.CognitoLocalFederationConfig || options.ownedPostgres != nil ||
		options.battleCapabilities != nil || options.configureInstances != nil ||
		options.closeInstances != nil || options.terminalSessions != nil ||
		options.commandAPI != nil || options.instanceStackLaunchConfigured != nil {
		return LocalCognitoListeners{}, nil, errors.New("Cognito hosted login requires standalone signed local federation configuration")
	}
	if err := cognitoidp.ValidateLocalHostedOrigin(origin); err != nil {
		return LocalCognitoListeners{}, nil, errors.New("Cognito hosted login requires a canonical numeric-loopback HTTPS origin")
	}
	var browser cognitoidp.HostedBrowser
	awsHandler, closeState, err := newWithProviders(path, options, func(store *cognitoidp.Store) error {
		var openErr error
		browser, openErr = cognitoidp.NewHostedBrowser(store, localCognitoAccount, origin)
		return openErr
	})
	if err != nil {
		return LocalCognitoListeners{}, nil, err
	}
	return LocalCognitoListeners{AWS: awsHandler, Browser: browser}, closeState, nil
}
