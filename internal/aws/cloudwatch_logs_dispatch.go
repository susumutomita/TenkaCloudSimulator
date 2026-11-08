package aws

import (
	"errors"
	"net/http"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func isCloudWatchLogs(r *http.Request) bool {
	if strings.HasPrefix(r.Header.Get("X-Amz-Target"), "Logs_20140328.") || r.Header.Get("X-Simulator-Service") == "logs" {
		return true
	}
	_, scope, ok := strings.Cut(r.Header.Get("Authorization"), "Credential=")
	parts := strings.Split(scope, "/")
	return ok && len(parts) >= 4 && parts[3] == "logs"
}
func (g *Gateway) serveCloudWatchLogs(w http.ResponseWriter, r *http.Request) {
	if g.CloudWatchLogs == nil {
		logsWriteError(w, logsFailure{"UnknownOperationException", "CloudWatch Logs state is not configured."})
		return
	}
	region := r.Header.Get("X-Simulator-Region")
	if _, scope, ok := strings.Cut(r.Header.Get("Authorization"), "Credential="); ok {
		parts := strings.Split(scope, "/")
		if len(parts) >= 3 {
			if region != "" && region != parts[2] {
				logsWriteError(w, logsInvalid("Region header and credential scope disagree."))
				return
			}
			region = parts[2]
		}
	}
	if region == "" {
		region = "us-east-1"
	}
	principal := r.Header.Get("X-Simulator-Principal")
	if principal == "" {
		principal = core.Administrator
	}
	g.CloudWatchLogs.ServeHTTP(w, r, region, func(action, resource string) error {
		if g.Identities == nil {
			return errors.New("Logs identity authority is not configured")
		}
		decision, err := g.Identities.DecisionContext(r.Context(), principal, action, resource)
		if err != nil {
			return err
		}
		w.Header().Set("X-Simulator-IAM-Decision", decision)
		if decision != "allowed" {
			return logsFailure{"AccessDeniedException", "IAM evaluation: " + decision}
		}
		return nil
	})
}
