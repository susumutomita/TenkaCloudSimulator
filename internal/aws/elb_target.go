package aws

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/apirouting"
	pythonruntime "github.com/susumutomita/TenkaCloudSimulator/internal/workload/python"
)

// The ALB Lambda event and response follow the documented Lambda-target
// shapes with multi-value headers disabled. Bounds are local: the simulator's
// 256 KiB Lambda event limit, not the AWS 1 MB body limit.
const elbEventBytes = apirouting.MaxProxyEventBytes

var (
	errELBTargetTimeout = errors.New("the Lambda target did not respond before the deadline")
	errELBTargetFailed  = errors.New("the Lambda target invocation failed")
	errELBInternal      = errors.New("the simulator could not read or run the target")
)

type elbEventContext struct {
	ELB struct {
		TargetGroupArn string `json:"targetGroupArn"`
	} `json:"elb"`
}
type elbEvent struct {
	RequestContext        elbEventContext   `json:"requestContext"`
	HTTPMethod            string            `json:"httpMethod"`
	Path                  string            `json:"path"`
	QueryStringParameters map[string]string `json:"queryStringParameters"`
	Headers               map[string]string `json:"headers"`
	Body                  string            `json:"body"`
	IsBase64Encoded       bool              `json:"isBase64Encoded"`
}

type elbLambdaResponse struct {
	StatusCode        *int              `json:"statusCode"`
	StatusDescription string            `json:"statusDescription"`
	Headers           map[string]string `json:"headers"`
	Body              string            `json:"body"`
	IsBase64Encoded   bool              `json:"isBase64Encoded"`
}

// elbParseResponse admits only the documented single-value response fields.
// Unknown fields, including multiValueHeaders while that attribute is not
// simulated, make the response malformed rather than silently ignored.
func elbParseResponse(payload []byte) (int, map[string]string, []byte, error) {
	if len(payload) > elbEventBytes || !utf8.Valid(payload) {
		return 0, nil, nil, errELBTargetFailed
	}
	if !elbExactFields(payload, "statusCode", "statusDescription", "headers", "body", "isBase64Encoded") {
		return 0, nil, nil, errELBTargetFailed
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var response elbLambdaResponse
	if decoder.Decode(&response) != nil || decoder.Decode(&struct{}{}) != io.EOF || response.StatusCode == nil {
		return 0, nil, nil, errELBTargetFailed
	}
	status := *response.StatusCode
	if status < 200 || status > 599 {
		return 0, nil, nil, errELBTargetFailed
	}
	for name, value := range response.Headers {
		if !elbHeader(name, value) {
			return 0, nil, nil, errELBTargetFailed
		}
	}
	body := []byte(response.Body)
	if response.IsBase64Encoded {
		decoded, err := base64.StdEncoding.DecodeString(response.Body)
		if err != nil {
			return 0, nil, nil, errELBTargetFailed
		}
		body = decoded
	}
	return status, response.Headers, body, nil
}

// elbExactFields requires one top-level object whose member names are spelled
// exactly as documented and appear at most once. encoding/json alone would
// accept other letter cases and silently keep the last duplicate.
func elbExactFields(payload []byte, allowed ...string) bool {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return false
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		name, ok := token.(string)
		if err != nil || !ok || seen[name] || !slices.Contains(allowed, name) {
			return false
		}
		seen[name] = true
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return false
		}
	}
	return true
}
func elbHeader(name, value string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", r)) {
			return false
		}
	}
	for _, r := range value {
		if r < 32 && r != '\t' || r == 127 {
			return false
		}
	}
	return true
}

// elbRawQuery keeps names and values exactly as sent: the load balancer does
// not URL-decode them. Duplicate names keep the last value.
func elbRawQuery(raw string) (map[string]string, error) {
	result := map[string]string{}
	if raw == "" {
		return result, nil
	}
	for _, pair := range strings.Split(raw, "&") {
		if pair == "" {
			continue
		}
		key, value, _ := strings.Cut(pair, "=")
		if !utf8.ValidString(key) || !utf8.ValidString(value) {
			return nil, errors.New("invalid query text")
		}
		result[key] = value
	}
	return result, nil
}

func elbBuildEvent(r *http.Request, targetGroup, path string, port int, body []byte) ([]byte, error) {
	headers, err := apirouting.ApplicationHeaders(r.Header)
	if err != nil {
		return nil, err
	}
	query, err := elbRawQuery(r.URL.RawQuery)
	if err != nil {
		return nil, err
	}
	headers["host"] = r.Host
	client := r.RemoteAddr
	if host, _, splitErr := net.SplitHostPort(r.RemoteAddr); splitErr == nil {
		client = host
	}
	if previous := headers["x-forwarded-for"]; previous != "" {
		client = previous + ", " + client
	}
	headers["x-forwarded-for"], headers["x-forwarded-proto"], headers["x-forwarded-port"] = client, "http", strconv.Itoa(port)
	event := elbEvent{HTTPMethod: r.Method, Path: path, QueryStringParameters: query, Headers: headers}
	event.RequestContext.ELB.TargetGroupArn = targetGroup
	if utf8.Valid(body) {
		event.Body = string(body)
	} else {
		event.Body, event.IsBase64Encoded = base64.StdEncoding.EncodeToString(body), true
	}
	data, err := json.Marshal(event)
	if err != nil || len(data) > elbEventBytes {
		return nil, errors.New("encoded load balancer event exceeds the local Lambda event limit")
	}
	return data, nil
}

func elbHealthEvent(targetGroup, path string) []byte {
	event := elbEvent{HTTPMethod: "GET", Path: path, QueryStringParameters: map[string]string{}, Headers: map[string]string{"user-agent": "ELB-HealthChecker/2.0"}}
	event.RequestContext.ELB.TargetGroupArn = targetGroup
	data, _ := json.Marshal(event)
	return data
}

// elbGuestContext keeps the caller's cancellation but none of its values, so
// an SDK session policy or identity attached to the incoming request can never
// replace the saved execution role in the guest's bounded SDK calls.
func elbGuestContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	stop := context.AfterFunc(parent, cancel)
	return ctx, func() { stop(); cancel() }
}

// elbTargetError separates target configuration failures (missing function,
// permission or trusted role) from simulator storage or runtime failures.
func elbTargetError(err error, missing ...error) error {
	var failure ssmFailure
	if errors.As(err, &failure) || slices.ContainsFunc(missing, func(e error) bool { return errors.Is(err, e) }) {
		return errELBTargetFailed
	}
	return errELBInternal
}

// invokeELBTarget runs the registered function with Elastic Load Balancing's
// authority: the function's resource policy must allow this exact target group.
// Function, policy and execution role are captured in one read transaction,
// which closes before guest code runs and may call the same database.
func (g *Gateway) invokeELBTarget(ctx context.Context, region, targetGroup, function string, event []byte, limit time.Duration) ([]byte, error) {
	name, err := elbFunctionName(region, function)
	if err != nil {
		return nil, errELBTargetFailed
	}
	if g.Lambdas == nil {
		return nil, errELBInternal
	}
	tx, err := g.Lambdas.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, errELBInternal
	}
	defer tx.Rollback()
	var data string
	if err = tx.QueryRowContext(ctx, "SELECT document FROM aws_lambda_functions WHERE region=? AND name=?", region, name).Scan(&data); err != nil {
		return nil, elbTargetError(err, sql.ErrNoRows)
	}
	var saved LambdaFunction
	if err = json.Unmarshal([]byte(data), &saved); err != nil || saved.Name != name || saved.Region != region {
		return nil, errELBInternal
	}
	policy, _, err := lambdaPolicyRead(ctx, tx, region, name)
	if err != nil {
		return nil, elbTargetError(err)
	}
	if !lambdaPolicyAllowsELB(policy, region, name, targetGroup) {
		return nil, errELBTargetFailed
	}
	if err = iamExecutionRole(ctx, tx, saved.Role); err != nil {
		return nil, elbTargetError(err)
	}
	if err = tx.Commit(); err != nil {
		return nil, errELBInternal
	}
	timeout := time.Duration(saved.Timeout) * time.Second
	bounded := limit > 0 && limit <= timeout
	if bounded {
		timeout = limit
	}
	guest, cancel := elbGuestContext(ctx)
	defer cancel()
	// One-time runtime initialization is not charged to the target deadline.
	runner, err := g.Lambdas.runner(guest)
	if err != nil {
		return nil, errELBInternal
	}
	result, err := runner.Run(guest, pythonruntime.Invocation{Exchange: g.lambdaExchange(saved, region), Modules: lambdaSDKFiles(), Code: saved.Code, Handler: saved.Handler, Event: event, Environment: saved.Environment, Context: map[string]string{"function_name": saved.Name, "function_version": "$LATEST", "invoked_function_arn": lambdaARN(region, name)}, Timeout: timeout})
	if err != nil && bounded && errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
		return nil, errELBTargetTimeout // The health check timeout, not the function's own.
	}
	if err != nil || result.FunctionError {
		return nil, errELBTargetFailed
	}
	return result.Payload, nil
}

func elbPlainError(w http.ResponseWriter, status int, reason string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Simulator-ELB-Reason", reason)
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, "%d %s\n", status, http.StatusText(status))
}

type elbRoute struct {
	listener ELBListener
	target   string
}

// resolveELBRoute reads one consistent snapshot of the route: load balancer,
// listener, action and the registered target.
func (s *ELBStore) resolveELBRoute(ctx context.Context, region, name string, port int) (elbRoute, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return elbRoute{}, err
	}
	defer tx.Rollback()
	lb, err := elbReadLoadBalancer(ctx, tx, "region=? AND name=?", region, name)
	if err != nil {
		return elbRoute{}, err
	}
	route := elbRoute{}
	if route.listener, err = elbReadListener(ctx, tx, "load_balancer=? AND port=?", lb.ARN, port); err != nil {
		return elbRoute{}, err
	}
	if route.listener.Action.Type == "forward" {
		err = tx.QueryRowContext(ctx, "SELECT function_arn FROM aws_elb_targets WHERE target_group=?", route.listener.Action.TargetGroupARN).Scan(&route.target)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return elbRoute{}, err
		}
	}
	return route, tx.Commit()
}

var elbMethods = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}

// serveELBTraffic is the local load balancer data plane. Host token
// authentication precedes it; no IAM action applies, as for a real listener.
func (g *Gateway) serveELBTraffic(w http.ResponseWriter, r *http.Request) {
	parts := strings.SplitN(strings.TrimPrefix(r.URL.EscapedPath(), elbDataPrefix), "/", 4)
	if len(parts) < 3 || !elbNamePattern.MatchString(parts[1]) {
		elbPlainError(w, 404, "Use /_elb/{region}/{load-balancer-name}/{port}/{path}.")
		return
	}
	region, name := parts[0], parts[1]
	port, err := strconv.Atoi(parts[2])
	if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != parts[2] {
		elbPlainError(w, 404, "Invalid listener port.")
		return
	}
	if selected := r.Header.Get("X-Simulator-Region"); selected != "" && selected != region || !slices.Contains(Regions, region) {
		elbPlainError(w, 404, "Unsupported or mismatched local region.")
		return
	}
	if !slices.Contains(elbMethods, r.Method) {
		elbPlainError(w, 405, "Unsupported request method.")
		return
	}
	if g.ELB == nil {
		elbPlainError(w, 503, "Elastic Load Balancing state is not configured.")
		return
	}
	path := "/"
	if len(parts) == 4 {
		path += parts[3]
	}
	route, err := g.ELB.resolveELBRoute(r.Context(), region, name, port)
	if errors.Is(err, sql.ErrNoRows) {
		elbPlainError(w, 404, "No load balancer has an HTTP listener on this name and port.")
		return
	} else if err != nil {
		elbPlainError(w, 500, "Load balancer state could not be read.")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, elbEventBytes))
	if err != nil {
		elbPlainError(w, 413, "Request body exceeds the local 256 KiB Lambda event limit.")
		return
	}
	action := route.listener.Action
	if action.Type == "fixed-response" {
		status, _ := strconv.Atoi(action.FixedStatus)
		headers := map[string]string{}
		elbWrite(w, r, status, headers, []byte(action.FixedBody))
		return
	}
	if route.target == "" {
		elbPlainError(w, 503, "The target group has no registered targets.")
		return
	}
	event, err := elbBuildEvent(r, action.TargetGroupARN, path, port, body)
	if err != nil {
		elbPlainError(w, 400, "Request headers, query or body cannot form a bounded Lambda event.")
		return
	}
	payload, err := g.invokeELBTarget(r.Context(), region, action.TargetGroupARN, route.target, event, 0)
	if errors.Is(err, errELBInternal) {
		elbPlainError(w, 500, "The simulator could not read or run the Lambda target.")
		return
	} else if err != nil {
		elbPlainError(w, 502, "The Lambda target failed, timed out, was not permitted or no longer exists.")
		return
	}
	status, headers, responseBody, err := elbParseResponse(payload)
	if err != nil {
		elbPlainError(w, 502, "The Lambda target returned a malformed or oversized response.")
		return
	}
	elbWrite(w, r, status, headers, responseBody)
}

// elbWrite deliberately differs from a live load balancer in the same way as
// the local REST proxy: guest or exercise output never executes on the console
// origin. The status code, safe headers and body remain inspectable.
func elbWrite(w http.ResponseWriter, r *http.Request, status int, headers map[string]string, body []byte) {
	for name, values := range apirouting.DownloadHeaders(apirouting.ProxyResponse{Headers: headers}) {
		w.Header().Del(name)
		for _, value := range values {
			w.Header().Add(name, value)
		}
	}
	w.WriteHeader(status)
	if r.Method != "HEAD" {
		_, _ = w.Write(body)
	}
}

type elbCheck struct {
	group, function string
	epoch           int64
	health          ELBHealthCheck
}

// RunELBHealthChecks performs at most limit due health checks sequentially and
// returns how many results were recorded. A result is recorded only when the
// target group epoch and registration still match the snapshot taken before
// the guest ran; registration, health-setting and listener changes discard it.
// Each check's next due time starts from that check's own start, read from now.
func (g *Gateway) RunELBHealthChecks(ctx context.Context, now func() time.Time, limit int) (int, error) {
	if g.ELB == nil {
		return 0, nil
	}
	checks, err := g.ELB.dueChecks(ctx, now(), limit)
	if err != nil {
		return 0, err
	}
	recorded := 0
	for _, check := range checks {
		region := elbTGARNPattern.FindStringSubmatch(check.group)[1]
		started := now()
		payload, err := g.invokeELBTarget(ctx, region, check.group, check.function, elbHealthEvent(check.group, check.health.Path), time.Duration(check.health.Timeout)*time.Second)
		reason, description := "", ""
		switch {
		case errors.Is(err, errELBTargetTimeout):
			reason, description = "Target.Timeout", "Request timed out"
		case errors.Is(err, errELBInternal):
			reason, description = "Elb.InternalError", "Health checks failed due to an internal error"
		case err != nil:
			reason, description = "Target.FailedHealthChecks", "Health checks failed"
		default:
			status, _, _, parseErr := elbParseResponse(payload)
			if parseErr != nil {
				reason, description = "Target.FailedHealthChecks", "Health checks failed"
			} else if !elbMatches(check.health.Matcher, status) {
				reason, description = "Target.ResponseCodeMismatch", fmt.Sprintf("Health checks failed with these codes: [%d]", status)
			}
		}
		if ctx.Err() != nil {
			return recorded, ctx.Err()
		}
		saved, err := g.ELB.recordCheck(ctx, check, started, reason, description)
		if err != nil {
			return recorded, err
		}
		if saved {
			recorded++
		}
	}
	return recorded, nil
}

func (s *ELBStore) dueChecks(ctx context.Context, now time.Time, limit int) ([]elbCheck, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT t.target_group,t.function_arn,t.epoch,g.document FROM aws_elb_targets t JOIN aws_elb_target_groups g ON g.arn=t.target_group
 WHERE g.checks=1 AND t.epoch=g.epoch AND t.due<=? AND EXISTS(SELECT 1 FROM aws_elb_listeners l WHERE l.target_group=t.target_group) ORDER BY t.due,t.target_group LIMIT ?`, now.Unix(), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []elbCheck
	for rows.Next() {
		var check elbCheck
		var data string
		if err = rows.Scan(&check.group, &check.function, &check.epoch, &data); err != nil {
			return nil, err
		}
		var tg ELBTargetGroup
		if err = json.Unmarshal([]byte(data), &tg); err != nil {
			return nil, err
		}
		check.health = tg.Health
		result = append(result, check)
	}
	return result, rows.Err()
}

// recordCheck applies one result to the consecutive success/failure counters
// with the configured thresholds, guarded by the snapshot epoch.
func (s *ELBStore) recordCheck(ctx context.Context, check elbCheck, now time.Time, reason, description string) (bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var state, currentReason, currentDescription string
	var successes, failures int
	err = tx.QueryRowContext(ctx, `SELECT t.state,t.reason,t.description,t.successes,t.failures FROM aws_elb_targets t JOIN aws_elb_target_groups g ON g.arn=t.target_group
 WHERE t.target_group=? AND t.function_arn=? AND t.epoch=? AND g.epoch=? AND g.checks=1`, check.group, check.function, check.epoch, check.epoch).Scan(&state, &currentReason, &currentDescription, &successes, &failures)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil // A newer epoch or registration supersedes this result.
	} else if err != nil {
		return false, err
	}
	// Below a threshold the previous state and its reason are retained.
	if reason == "" {
		successes, failures = successes+1, 0
		if state == "healthy" || successes >= check.health.Healthy {
			state, currentReason, currentDescription = "healthy", "", ""
		}
	} else {
		successes, failures = 0, failures+1
		if state == "unhealthy" || failures >= check.health.Unhealthy {
			state, currentReason, currentDescription = "unhealthy", reason, description
		}
	}
	due := now.Add(time.Duration(check.health.Interval) * time.Second).Unix()
	if _, err = tx.ExecContext(ctx, "UPDATE aws_elb_targets SET state=?,reason=?,description=?,successes=?,failures=?,due=? WHERE target_group=?", state, currentReason, currentDescription, successes, failures, due, check.group); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// ELBHealthLoop runs due checks until ctx ends. Each pass is bounded and runs
// guest code only for health-enabled Lambda target groups used by a listener.
func (g *Gateway) ELBHealthLoop(ctx context.Context, tick time.Duration) {
	ticker := time.NewTicker(tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Failures are not hidden as health: an unreadable configuration or
			// store leaves the recorded state unchanged and is retried next tick.
			_, _ = g.RunELBHealthChecks(ctx, time.Now, 4)
		}
	}
}
