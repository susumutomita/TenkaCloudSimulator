package cognitoidp

import (
	"context"
	"net/http"
	"net/url"
)

// HostedServiceProvider is the public half of the active local SAML SP for a
// canonical pool. The signing key remains private to HostedBrowser.
type HostedServiceProvider struct {
	EntityID              string
	ACS                   string
	MetadataURL           string
	SigningCertificateDER []byte
}

// HostedBrowser handles only its fixed direct-loopback TLS federation routes.
// Run as the sole handler of a dedicated local HTTPS listener. Its JWTs do not
// authenticate any simulator API or console.
type HostedBrowser interface {
	http.Handler
	ServiceProvider(ctx context.Context, poolID string) (HostedServiceProvider, error)
}

// ValidateLocalHostedOrigin allows the server to reject a bad local profile
// before touching any database. NewHostedBrowser enforces the same bound.
func ValidateLocalHostedOrigin(origin string) error {
	_, err := parseLocalHostedOrigin(origin)
	return err
}

func parseLocalHostedOrigin(origin string) (*url.URL, error) {
	u, err := localFederationURL(origin+"/", false)
	if err != nil || u.Scheme != "https" || u.Scheme+"://"+u.Host != origin {
		return nil, errInvalid
	}
	return u, nil
}
