package aws

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestcommand"
)

// Only the retained runtime may resolve targets. Headers, comments and STS
// session names never select a VM incarnation or establish a deployment grant.
type SSMCommandNativeBackend interface {
	guestcommand.DurableBackend
	ResolveCommandTarget(context.Context, string, string) (EC2GuestLease, error)
}
type SSMCommandAPI struct {
	db      *sql.DB
	store   *SSMCommandStore
	backend SSMCommandNativeBackend
}

// NewSSMCommandAPI wraps the exact durable store returned by the canonical
// factory. It cannot pair a resolver with a different store/backend database.
func NewSSMCommandAPI(store *SSMCommandStore) (*SSMCommandAPI, error) {
	if store == nil || store.db == nil {
		return nil, ErrSSMCommandInvalid
	}
	backend, ok := store.backend.(SSMCommandNativeBackend)
	if !ok || backend == nil {
		return nil, ErrSSMCommandInvalid
	}
	if err := initNativeCommandPages(store.db); err != nil {
		return nil, err
	}
	return &SSMCommandAPI{db: store.db, store: store, backend: backend}, nil
}
func isNativeRunCommand(r *http.Request) bool {
	switch r.Header.Get("X-Amz-Target") {
	case "AmazonSSM.SendCommand", "AmazonSSM.GetCommandInvocation", "AmazonSSM.ListCommands":
		return true
	}
	return false
}

var nativeCommandRoleID = regexp.MustCompile(`^AROA[A-Z0-9]{16}$`)

func commandPrincipal(c ssmSessionCaller) (string, error) {
	role, session, ok := strings.Cut(c.identity.UserID, ":")
	if !ok || session == "" || !nativeCommandRoleID.MatchString(role) || c.identity.Account != "000000000000" {
		return "", ErrSSMCommandInvalid
	}
	// Stable role incarnation, not a renewable temporary key or job/session name.
	return role, nil
}

type commandPermission struct {
	action, resource string
	tags             map[string]string
}

func commandAuthorize(ctx context.Context, c ssmSessionCaller, action, resource string, tags map[string]string) error {
	return commandAuthorizeAll(ctx, c, []commandPermission{{action, resource, tags}})
}
func commandAuthorizeAll(ctx context.Context, c ssmSessionCaller, permissions []commandPermission) error {
	policy, err := c.snapshot(ctx)
	if err != nil {
		return err
	}
	for _, permission := range permissions {
		values := core.PolicyContext{"aws:userid": c.identity.UserID}
		for key, value := range permission.tags {
			values["aws:ResourceTag/"+key] = value
			values["ssm:resourceTag/"+key] = value
		}
		if core.EvaluateWithContext(policy, permission.action, permission.resource, values) != "allowed" {
			return ssmFailure{"AccessDeniedException", "The current role policy does not allow this command action."}
		}
	}
	return nil
}
func (a *SSMCommandAPI) ServeNative(w http.ResponseWriter, r *http.Request) {
	caller, err := bindSSMSessionCaller(r)
	if err != nil {
		ssmError(w, ssmFailure{"AccessDeniedException", "A verified assumed-role request is required."})
		return
	}
	if _, terminal := r.Context().Value(terminalBoundaryKey{}).(terminalBoundary); terminal {
		ssmError(w, ssmFailure{"AccessDeniedException", "Terminal handoffs cannot run host commands."})
		return
	}
	principal, err := commandPrincipal(caller)
	if err != nil {
		ssmError(w, ssmFailure{"AccessDeniedException", "A native local role incarnation is required."})
		return
	}
	if a == nil {
		ssmError(w, ssmFailure{"UnknownOperationException", "A qualified durable command backend is not configured."})
		return
	}
	if r.Method != "POST" || r.URL.Path != "/" || r.URL.RawPath != "" || r.URL.RawQuery != "" || len(r.Header.Values("Content-Type")) != 1 || r.Header.Get("Content-Type") != "application/x-amz-json-1.1" || len(r.Header.Values("X-Amz-Target")) != 1 || !isNativeRunCommand(r) {
		ssmError(w, ssmFailure{"ValidationException", "Use the exact regional SSM JSON root operation."})
		return
	}
	signature, err := parseSDKSignature(r)
	if err != nil || signature.service != "ssm" {
		ssmError(w, ssmFailure{"AccessDeniedException", "Signed SSM scope required."})
		return
	}
	region := signature.region
	if header := r.Header.Get("X-Simulator-Region"); header != "" && header != region {
		ssmError(w, ssmFailure{"ValidationException", "Signed region and region header disagree."})
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 48<<10))
	if err != nil {
		ssmError(w, ssmFailure{"ValidationException", "Command requests are limited to 48 KiB."})
		return
	}
	operation := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "AmazonSSM.")
	input, err := decodeNativeCommand(operation, raw)
	if err != nil {
		ssmError(w, ssmFailure{"ValidationException", "Unsupported or ambiguous bounded command request."})
		return
	}
	ctx, cancel := context.WithDeadline(r.Context(), caller.identity.Expires)
	defer cancel()
	switch operation {
	case "SendCommand":
		record, err := a.send(ctx, caller, principal, region, input.Send)
		if err != nil {
			nativeCommandError(w, err)
			return
		}
		ssmJSON(w, 200, map[string]any{"Command": commandWireSummary(record.CommandId, record.Request, record.RequestedAt)})
	case "GetCommandInvocation":
		if err = commandAuthorize(ctx, caller, "ssm:GetCommandInvocation", "*", nil); err != nil {
			nativeCommandError(w, err)
			return
		}
		authority, err := a.retainedAuthority(ctx, principal, region, input.CommandID)
		if err != nil {
			nativeCommandError(w, err)
			return
		}
		record, err := a.store.Reconcile(ctx, authority, input.CommandID, input.InstanceID)
		if err != nil {
			nativeCommandError(w, err)
			return
		}
		var output nativeCommandInvocation
		found := false
		for _, target := range record.Targets {
			if target.Lease.Owner.Instance == input.InstanceID {
				output, err = commandWireInvocation(record, input.InstanceID, target.Observation)
				found = true
				break
			}
		}
		if !found {
			err = ErrSSMCommandInvalid
		}
		if err != nil {
			nativeCommandError(w, err)
			return
		}
		if err = commandAuthorize(ctx, caller, "ssm:GetCommandInvocation", "*", nil); err != nil {
			nativeCommandError(w, err)
			return
		}
		ssmJSON(w, 200, output)
	case "ListCommands":
		if err = commandAuthorize(ctx, caller, "ssm:ListCommands", "*", nil); err != nil {
			nativeCommandError(w, err)
			return
		}
		summaries, next, err := a.list(ctx, principal, region, input)
		if err != nil {
			nativeCommandError(w, err)
			return
		}
		if err = commandAuthorize(ctx, caller, "ssm:ListCommands", "*", nil); err != nil {
			nativeCommandError(w, err)
			return
		}
		ssmJSON(w, 200, map[string]any{"Commands": summaries, "NextToken": next})
	}
}
func nativeCommandError(w http.ResponseWriter, err error) {
	if errors.Is(err, ErrSSMCommandUnknown) {
		ssmJSON(w, 500, map[string]string{"__type": "InternalServerError", "message": "Command outcome is unknown. Reconcile with ListCommands/GetCommandInvocation; do not resubmit."})
		return
	}
	if errors.Is(err, ErrSSMCommandInvalid) || errors.Is(err, sql.ErrNoRows) {
		ssmError(w, ssmFailure{"InvalidCommandId", "Command or exact target scope is unavailable."})
		return
	}
	ssmError(w, err)
}
func (a *SSMCommandAPI) send(ctx context.Context, c ssmSessionCaller, principal, region string, input SSMCommandRequest) (SSMCommand, error) {
	if err := commandAuthorize(ctx, c, "ssm:SendCommand", "arn:aws:ssm:"+region+"::document/AWS-RunShellScript", nil); err != nil {
		return SSMCommand{}, err
	}
	permissions := []commandPermission{{action: "ssm:SendCommand", resource: "arn:aws:ssm:" + region + "::document/AWS-RunShellScript"}}
	leases := make([]EC2GuestLease, 0, len(input.InstanceIds))
	deployment := ""
	for _, id := range input.InstanceIds {
		lease, err := a.backend.ResolveCommandTarget(ctx, region, id)
		if err != nil {
			return SSMCommand{}, err
		}
		if !validGuestRecord(lease) || lease.State != "held" || lease.Owner.Account != "000000000000" || lease.Owner.Region != region || lease.Owner.Instance != id {
			return SSMCommand{}, ErrSSMCommandInvalid
		}
		if deployment != "" && deployment != lease.Owner.Deployment {
			return SSMCommand{}, fmt.Errorf("cross-deployment command targets are unsupported")
		}
		deployment = lease.Owner.Deployment
		tags, err := a.instanceTags(ctx, lease)
		if err != nil {
			return SSMCommand{}, err
		}
		if err = commandAuthorize(ctx, c, "ssm:SendCommand", networkARN(region, "instance", id), tags); err != nil {
			return SSMCommand{}, err
		}
		permissions = append(permissions, commandPermission{action: "ssm:SendCommand", resource: networkARN(region, "instance", id), tags: tags})
		leases = append(leases, lease)
	}
	// Re-evaluate the complete document+target set against ONE current policy.
	// Separately allowed snapshots must never be combined into a new grant.
	if err := commandAuthorizeAll(ctx, c, permissions); err != nil {
		return SSMCommand{}, err
	}
	authority := SSMCommandAuthority{Account: "000000000000", Region: region, Principal: principal, Deployment: deployment}
	// Existing Send is the sole durable-before-dispatch implementation. Never
	// retry it after an error, even if the response body/connection was lost.
	return a.store.Send(ctx, authority, input, leases)
}
