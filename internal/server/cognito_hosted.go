package server

import (
	"net/http"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/cognitoidp"
)

const localCognitoAccount = "000000000000"

// Only the concrete access middleware can dispatch browser requests. A caller
// of the trusted composition alone gets its original authenticated AWS
// provider API with no hosted paths and no implicit browser access.
type cognitoHostedComposition struct {
	http.Handler
	browser cognitoidp.HostedBrowser
}
