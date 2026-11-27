package server

import (
	"net"
	"net/http"

	"github.com/susumutomita/TenkaCloudSimulator/internal/provider"
)

// This is presentation-only discovery, not an IAM grant or a VM health check.
// The unchanged CloudFormation endpoint authorizes and performs the launch.
func addEC2InstanceStackLaunchCapability(w http.ResponseWriter, r *http.Request, capabilities map[string]any, configured func() bool) {
	actor, trusted := provider.ActorFromContext(r.Context())
	peer, _, err := net.SplitHostPort(r.RemoteAddr)
	if !trusted || actor.CredentialRole != provider.CredentialOperator ||
		actor.CredentialIdentity != "simulator-admin" || actor.ExerciseIdentity != "simulator-admin" ||
		w.Header().Get("X-Simulator-Authentication") != "required" || consoleScopeFromRequest(r) != nil ||
		err != nil || !net.ParseIP(peer).IsLoopback() || configured == nil || !configured() {
		return
	}
	ec2 := capabilities["services"].(map[string]any)["ec2"].(map[string]any)
	ec2["instanceStackLaunch"] = "CloudFormation/CreateStack"
}
