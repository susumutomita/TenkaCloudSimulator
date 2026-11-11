package aws

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// ELBStore owns bounded Application Load Balancer control-plane state in the
// provider SQLite database. It performs no network placement: subnets and
// security groups are syntactic references, and only Lambda targets can run.
// Hosts must allocate a separate database, process and token authority per team.
type ELBStore struct{ db *sql.DB }

type elbFailure struct {
	code, message string
	status        int
}

func (e elbFailure) Error() string { return e.message }
func elbErr(code, message string) error {
	return elbFailure{code, message, 400}
}
func elbInvalid(message string) error { return elbErr("ValidationError", message) }
func elbConfig(message string) error  { return elbErr("InvalidConfigurationRequest", message) }
func elbDenied(message string) error  { return elbFailure{"AccessDenied", message, 403} }

// Local bounds. They are exercise limits, not AWS quotas.
const (
	elbLoadBalancerLimit = 50
	elbTargetGroupLimit  = 100
	elbListenerLimit     = 10
	elbAccount           = "000000000000"
)

type ELBLoadBalancer struct {
	ARN, Name, ID, Region, Scheme, DNSName, Created string
	Subnets, SecurityGroups                         []string
}

type ELBHealthCheck struct {
	Enabled                               bool
	Path, Matcher                         string
	Interval, Timeout, Healthy, Unhealthy int
}

type ELBTargetGroup struct {
	ARN, Name, ID, Region, Created string
	Health                         ELBHealthCheck
	LoadBalancerARNs               []string `json:"-"`
}

// ELBAction is one listener default action. Only forward to one Lambda target
// group and fixed-response are executable; other official types are refused.
type ELBAction struct {
	Type, TargetGroupARN                     string
	Order, Weight                            int
	FixedStatus, FixedContentType, FixedBody string
}

type ELBListener struct {
	ARN, LoadBalancerARN, Region, Protocol string
	Port                                   int
	Action                                 ELBAction
}

// ELBTargetHealth is the reported state of one target description.
type ELBTargetHealth struct {
	Target, State, Reason, Description string
}

var (
	elbNamePattern   = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,30}[A-Za-z0-9])?$`)
	elbSubnetPattern = regexp.MustCompile(`^subnet-(?:[0-9a-f]{8}|[0-9a-f]{17})$`)
	elbGroupPattern  = regexp.MustCompile(`^sg-(?:[0-9a-f]{8}|[0-9a-f]{17})$`)
	elbLBARNPattern  = regexp.MustCompile(`^arn:aws:elasticloadbalancing:([a-z0-9-]{1,32}):000000000000:loadbalancer/app/([A-Za-z0-9-]{1,32})/([0-9a-f]{16})$`)
	elbTGARNPattern  = regexp.MustCompile(`^arn:aws:elasticloadbalancing:([a-z0-9-]{1,32}):000000000000:targetgroup/([A-Za-z0-9-]{1,32})/([0-9a-f]{16})$`)
	elbLsARNPattern  = regexp.MustCompile(`^arn:aws:elasticloadbalancing:([a-z0-9-]{1,32}):000000000000:listener/app/([A-Za-z0-9-]{1,32})/([0-9a-f]{16})/([0-9a-f]{16})$`)
	elbFnARNPattern  = regexp.MustCompile(`^arn:aws:lambda:([a-z0-9-]{1,32}):000000000000:function:([a-zA-Z0-9_-]{1,64})$`)
)

func NewELBStore(db *sql.DB) (*ELBStore, error) {
	_, e := db.Exec(`CREATE TABLE IF NOT EXISTS aws_elb_load_balancers(arn TEXT PRIMARY KEY,region TEXT NOT NULL,name TEXT NOT NULL,document TEXT NOT NULL,UNIQUE(region,name));
 CREATE TABLE IF NOT EXISTS aws_elb_target_groups(arn TEXT PRIMARY KEY,region TEXT NOT NULL,name TEXT NOT NULL,document TEXT NOT NULL,epoch INTEGER NOT NULL,checks INTEGER NOT NULL,UNIQUE(region,name));
 CREATE TABLE IF NOT EXISTS aws_elb_listeners(arn TEXT PRIMARY KEY,region TEXT NOT NULL,load_balancer TEXT NOT NULL REFERENCES aws_elb_load_balancers(arn) ON DELETE CASCADE,port INTEGER NOT NULL,target_group TEXT REFERENCES aws_elb_target_groups(arn),document TEXT NOT NULL,UNIQUE(load_balancer,port));
 CREATE TABLE IF NOT EXISTS aws_elb_targets(target_group TEXT PRIMARY KEY REFERENCES aws_elb_target_groups(arn) ON DELETE CASCADE,function_arn TEXT NOT NULL,epoch INTEGER NOT NULL,state TEXT NOT NULL,reason TEXT NOT NULL,description TEXT NOT NULL,successes INTEGER NOT NULL,failures INTEGER NOT NULL,due INTEGER NOT NULL);`)
	if e != nil {
		return nil, e
	}
	return &ELBStore{db}, nil
}

func elbID() (string, error) {
	b := make([]byte, 8)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return hex.EncodeToString(b), nil
}
func elbPrefix(region string) string {
	return "arn:aws:elasticloadbalancing:" + region + ":" + elbAccount + ":"
}
func elbLoadBalancerARN(region, name, id string) string {
	return elbPrefix(region) + "loadbalancer/app/" + name + "/" + id
}
func elbTargetGroupARN(region, name, id string) string {
	return elbPrefix(region) + "targetgroup/" + name + "/" + id
}

func elbValidName(name string, loadBalancer bool) error {
	if !elbNamePattern.MatchString(name) || loadBalancer && strings.HasPrefix(strings.ToLower(name), "internal-") {
		return elbInvalid("Names must be 1 to 32 alphanumeric characters or hyphens, must not begin or end with a hyphen, and load balancer names must not begin with internal-.")
	}
	return nil
}

// elbARN checks syntax and the request region. Foreign regions are refused
// explicitly rather than treated as another region's state.
func elbARN(pattern *regexp.Regexp, region, arn, kind string) error {
	parts := pattern.FindStringSubmatch(arn)
	if parts == nil || parts[1] != region {
		return elbInvalid("'" + elbQuote(arn) + "' is not a valid " + kind + " ARN in this region.")
	}
	return nil
}
func elbQuote(value string) string {
	if len(value) > 256 || !validText(value) {
		return "<invalid>"
	}
	return value
}

// ELBFunctionName resolves an unqualified same-region local Lambda ARN.
func elbFunctionName(region, arn string) (string, error) {
	parts := elbFnARNPattern.FindStringSubmatch(arn)
	if parts == nil || parts[1] != region {
		return "", elbErr("InvalidTarget", "Lambda targets must be unqualified local function ARNs in this region.")
	}
	return parts[2], nil
}

func elbDefaultHealth() ELBHealthCheck {
	return ELBHealthCheck{Enabled: false, Path: "/", Matcher: "200", Interval: 35, Timeout: 30, Healthy: 5, Unhealthy: 5}
}

// elbMatcher parses an ALB HTTP matcher: codes or ranges from 200 to 499.
func elbMatcher(value string) ([][2]int, error) {
	invalid := elbInvalid("Matcher HttpCode must be comma-separated codes or ranges between 200 and 499.")
	if value == "" || len(value) > 64 {
		return nil, invalid
	}
	var ranges [][2]int
	for _, part := range strings.Split(value, ",") {
		low, high, isRange := strings.Cut(part, "-")
		if !isRange {
			high = low
		}
		a, e1 := strconv.Atoi(low)
		b, e2 := strconv.Atoi(high)
		if e1 != nil || e2 != nil || len(low) != 3 || len(high) != 3 || a < 200 || b > 499 || a > b {
			return nil, invalid
		}
		ranges = append(ranges, [2]int{a, b})
	}
	return ranges, nil
}
func elbMatches(matcher string, status int) bool {
	ranges, err := elbMatcher(matcher)
	if err != nil {
		return false
	}
	for _, r := range ranges {
		if status >= r[0] && status <= r[1] {
			return true
		}
	}
	return false
}
func elbValidHealth(h ELBHealthCheck) error {
	if !strings.HasPrefix(h.Path, "/") || len(h.Path) > 1024 {
		return elbInvalid("HealthCheckPath must start with / and contain at most 1024 characters.")
	}
	for i := 0; i < len(h.Path); i++ {
		if h.Path[i] <= 32 || h.Path[i] >= 127 {
			return elbInvalid("HealthCheckPath must be printable ASCII without spaces.")
		}
	}
	if h.Interval < 5 || h.Interval > 300 || h.Timeout < 2 || h.Timeout > 120 || h.Healthy < 2 || h.Healthy > 10 || h.Unhealthy < 2 || h.Unhealthy > 10 {
		return elbInvalid("Health check interval must be 5-300, timeout 2-120 and thresholds 2-10.")
	}
	if h.Timeout >= h.Interval {
		return elbConfig("Health check timeout must be smaller than the interval.")
	}
	_, err := elbMatcher(h.Matcher)
	return err
}

type elbQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func elbNow() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

func elbReadLoadBalancer(ctx context.Context, q elbQuery, where string, args ...any) (ELBLoadBalancer, error) {
	var data string
	if err := q.QueryRowContext(ctx, "SELECT document FROM aws_elb_load_balancers WHERE "+where, args...).Scan(&data); err != nil {
		return ELBLoadBalancer{}, err
	}
	var lb ELBLoadBalancer
	if err := json.Unmarshal([]byte(data), &lb); err != nil {
		return ELBLoadBalancer{}, err
	}
	return lb, nil
}
func elbReadTargetGroup(ctx context.Context, q elbQuery, where string, args ...any) (ELBTargetGroup, int64, error) {
	var data string
	var epoch int64
	if err := q.QueryRowContext(ctx, "SELECT document,epoch FROM aws_elb_target_groups WHERE "+where, args...).Scan(&data, &epoch); err != nil {
		return ELBTargetGroup{}, 0, err
	}
	var tg ELBTargetGroup
	if err := json.Unmarshal([]byte(data), &tg); err != nil {
		return ELBTargetGroup{}, 0, err
	}
	rows, err := q.QueryContext(ctx, "SELECT DISTINCT load_balancer FROM aws_elb_listeners WHERE target_group=? ORDER BY load_balancer", tg.ARN)
	if err != nil {
		return ELBTargetGroup{}, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var arn string
		if err = rows.Scan(&arn); err != nil {
			return ELBTargetGroup{}, 0, err
		}
		tg.LoadBalancerARNs = append(tg.LoadBalancerARNs, arn)
	}
	return tg, epoch, rows.Err()
}
func elbReadListener(ctx context.Context, q elbQuery, where string, args ...any) (ELBListener, error) {
	var data string
	if err := q.QueryRowContext(ctx, "SELECT document FROM aws_elb_listeners WHERE "+where, args...).Scan(&data); err != nil {
		return ELBListener{}, err
	}
	var listener ELBListener
	if err := json.Unmarshal([]byte(data), &listener); err != nil {
		return ELBListener{}, err
	}
	return listener, nil
}

func elbSameSet(a, b []string) bool {
	x, y := slices.Clone(a), slices.Clone(b)
	slices.Sort(x)
	slices.Sort(y)
	return slices.Equal(x, y)
}

// elbBump starts a new health epoch: any in-flight check result recorded for an
// older epoch is discarded, and the registered target restarts initial checks.
func elbBump(ctx context.Context, tx *sql.Tx, arn string, now time.Time) error {
	if _, err := tx.ExecContext(ctx, "UPDATE aws_elb_target_groups SET epoch=epoch+1 WHERE arn=?", arn); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, "UPDATE aws_elb_targets SET epoch=(SELECT epoch FROM aws_elb_target_groups WHERE arn=?),state='initial',reason='Elb.InitialHealthChecking',description='Initial health checking in progress.',successes=0,failures=0,due=? WHERE target_group=?", arn, now.Unix(), arn)
	return err
}
func elbInUse(ctx context.Context, q elbQuery, arn string) (bool, error) {
	var count int
	err := q.QueryRowContext(ctx, "SELECT count(*) FROM aws_elb_listeners WHERE target_group=?", arn).Scan(&count)
	return count > 0, err
}

func (s *ELBStore) CreateLoadBalancer(ctx context.Context, lb ELBLoadBalancer, authorize func(string, string) error) (ELBLoadBalancer, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ELBLoadBalancer{}, err
	}
	defer tx.Rollback()
	existing, err := elbReadLoadBalancer(ctx, tx, "region=? AND name=?", lb.Region, lb.Name)
	if err == nil {
		if err = authorize("elasticloadbalancing:CreateLoadBalancer", existing.ARN); err != nil {
			return ELBLoadBalancer{}, err
		}
		if existing.Scheme != lb.Scheme || !elbSameSet(existing.Subnets, lb.Subnets) || !elbSameSet(existing.SecurityGroups, lb.SecurityGroups) {
			return ELBLoadBalancer{}, elbErr("DuplicateLoadBalancerName", "A load balancer with the same name '"+lb.Name+"' exists, but with different settings.")
		}
		return existing, nil // Identical settings: the documented idempotent create.
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ELBLoadBalancer{}, err
	}
	if lb.ID, err = elbID(); err != nil {
		return ELBLoadBalancer{}, err
	}
	lb.ARN = elbLoadBalancerARN(lb.Region, lb.Name, lb.ID)
	if err = authorize("elasticloadbalancing:CreateLoadBalancer", lb.ARN); err != nil {
		return ELBLoadBalancer{}, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_elb_load_balancers WHERE region=?", lb.Region).Scan(&count); err != nil {
		return ELBLoadBalancer{}, err
	}
	if count >= elbLoadBalancerLimit {
		return ELBLoadBalancer{}, elbErr("TooManyLoadBalancers", fmt.Sprintf("This local region supports at most %d load balancers.", elbLoadBalancerLimit))
	}
	number, _ := strconv.ParseUint(lb.ID[:8], 16, 64)
	prefix := ""
	if lb.Scheme == "internal" {
		prefix = "internal-"
	}
	lb.DNSName = fmt.Sprintf("%s%s-%010d.%s.elb.simulator.invalid", prefix, strings.ToLower(lb.Name), number%10000000000, lb.Region)
	lb.Created = elbNow()
	data, _ := json.Marshal(lb)
	if _, err = tx.ExecContext(ctx, "INSERT INTO aws_elb_load_balancers(arn,region,name,document) VALUES(?,?,?,?)", lb.ARN, lb.Region, lb.Name, string(data)); err != nil {
		return ELBLoadBalancer{}, err
	}
	return lb, tx.Commit()
}

// DeleteLoadBalancer deletes the load balancer and its listeners atomically. A
// missing load balancer succeeds, as the official operation documents.
func (s *ELBStore) DeleteLoadBalancer(ctx context.Context, region, arn string, authorize func(string, string) error) error {
	if err := elbARN(elbLBARNPattern, region, arn, "load balancer"); err != nil {
		return err
	}
	if err := authorize("elasticloadbalancing:DeleteLoadBalancer", arn); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = elbReadLoadBalancer(ctx, tx, "arn=? AND region=?", arn, region); errors.Is(err, sql.ErrNoRows) {
		return tx.Commit()
	} else if err != nil {
		return err
	}
	groups, err := elbStrings(ctx, tx, "SELECT DISTINCT target_group FROM aws_elb_listeners WHERE load_balancer=? AND target_group IS NOT NULL", arn)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM aws_elb_listeners WHERE load_balancer=?", arn); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM aws_elb_load_balancers WHERE arn=?", arn); err != nil {
		return err
	}
	for _, group := range groups {
		if err = elbBump(ctx, tx, group, time.Now()); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func elbStrings(ctx context.Context, q elbQuery, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var value string
		if err = rows.Scan(&value); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, rows.Err()
}

func (s *ELBStore) CreateTargetGroup(ctx context.Context, tg ELBTargetGroup, authorize func(string, string) error) (ELBTargetGroup, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ELBTargetGroup{}, err
	}
	defer tx.Rollback()
	existing, _, err := elbReadTargetGroup(ctx, tx, "region=? AND name=?", tg.Region, tg.Name)
	if err == nil {
		if err = authorize("elasticloadbalancing:CreateTargetGroup", existing.ARN); err != nil {
			return ELBTargetGroup{}, err
		}
		if existing.Health != tg.Health {
			return ELBTargetGroup{}, elbErr("DuplicateTargetGroupName", "A target group with the same name '"+tg.Name+"' exists, but with different settings.")
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ELBTargetGroup{}, err
	}
	if tg.ID, err = elbID(); err != nil {
		return ELBTargetGroup{}, err
	}
	tg.ARN = elbTargetGroupARN(tg.Region, tg.Name, tg.ID)
	if err = authorize("elasticloadbalancing:CreateTargetGroup", tg.ARN); err != nil {
		return ELBTargetGroup{}, err
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_elb_target_groups WHERE region=?", tg.Region).Scan(&count); err != nil {
		return ELBTargetGroup{}, err
	}
	if count >= elbTargetGroupLimit {
		return ELBTargetGroup{}, elbErr("TooManyTargetGroups", fmt.Sprintf("This local region supports at most %d target groups.", elbTargetGroupLimit))
	}
	tg.Created = elbNow()
	data, _ := json.Marshal(tg)
	if _, err = tx.ExecContext(ctx, "INSERT INTO aws_elb_target_groups(arn,region,name,document,epoch,checks) VALUES(?,?,?,?,0,?)", tg.ARN, tg.Region, tg.Name, string(data), tg.Health.Enabled); err != nil {
		return ELBTargetGroup{}, err
	}
	return tg, tx.Commit()
}

// ModifyTargetGroup applies a complete, already merged health configuration and
// starts a new health epoch so no older check result can be recorded.
func (s *ELBStore) ModifyTargetGroup(ctx context.Context, region, arn string, change func(ELBHealthCheck) (ELBHealthCheck, error), authorize func(string, string) error) (ELBTargetGroup, error) {
	if err := elbARN(elbTGARNPattern, region, arn, "target group"); err != nil {
		return ELBTargetGroup{}, err
	}
	if err := authorize("elasticloadbalancing:ModifyTargetGroup", arn); err != nil {
		return ELBTargetGroup{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ELBTargetGroup{}, err
	}
	defer tx.Rollback()
	tg, _, err := elbTargetGroupFor(ctx, tx, region, arn)
	if err != nil {
		return ELBTargetGroup{}, err
	}
	if tg.Health, err = change(tg.Health); err != nil {
		return ELBTargetGroup{}, err
	}
	data, _ := json.Marshal(tg)
	if _, err = tx.ExecContext(ctx, "UPDATE aws_elb_target_groups SET document=?,checks=? WHERE arn=?", string(data), tg.Health.Enabled, arn); err != nil {
		return ELBTargetGroup{}, err
	}
	if err = elbBump(ctx, tx, arn, time.Now()); err != nil {
		return ELBTargetGroup{}, err
	}
	return tg, tx.Commit()
}
func elbTargetGroupFor(ctx context.Context, q elbQuery, region, arn string) (ELBTargetGroup, int64, error) {
	tg, epoch, err := elbReadTargetGroup(ctx, q, "arn=? AND region=?", arn, region)
	if errors.Is(err, sql.ErrNoRows) {
		return tg, 0, elbErr("TargetGroupNotFound", "One or more target groups not found.")
	}
	return tg, epoch, err
}

func (s *ELBStore) DeleteTargetGroup(ctx context.Context, region, arn string, authorize func(string, string) error) error {
	if err := elbARN(elbTGARNPattern, region, arn, "target group"); err != nil {
		return err
	}
	if err := authorize("elasticloadbalancing:DeleteTargetGroup", arn); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, _, err = elbReadTargetGroup(ctx, tx, "arn=? AND region=?", arn, region); errors.Is(err, sql.ErrNoRows) {
		return tx.Commit()
	} else if err != nil {
		return err
	}
	if used, err := elbInUse(ctx, tx, arn); err != nil {
		return err
	} else if used {
		return elbErr("ResourceInUse", "Target group '"+arn+"' is currently in use by a listener or a rule.")
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM aws_elb_target_groups WHERE arn=?", arn); err != nil {
		return err
	}
	return tx.Commit()
}

// RegisterTarget registers the single Lambda function of a target group after
// checking that the function exists and its resource policy lets Elastic Load
// Balancing invoke it for this target group. No caller Lambda permission is used.
func (s *ELBStore) RegisterTarget(ctx context.Context, region, arn, function string, authorize func(string, string) error) error {
	if err := elbARN(elbTGARNPattern, region, arn, "target group"); err != nil {
		return err
	}
	name, err := elbFunctionName(region, function)
	if err != nil {
		return err
	}
	if err = authorize("elasticloadbalancing:RegisterTargets", arn); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	_, epoch, err := elbTargetGroupFor(ctx, tx, region, arn)
	if err != nil {
		return err
	}
	var current string
	switch err = tx.QueryRowContext(ctx, "SELECT function_arn FROM aws_elb_targets WHERE target_group=?", arn).Scan(&current); {
	case err == nil && current == function:
		return tx.Commit()
	case err == nil:
		return elbErr("TooManyTargets", "A Lambda target group can have only one registered function.")
	case !errors.Is(err, sql.ErrNoRows):
		return err
	}
	policy, _, err := lambdaPolicyRead(ctx, tx, region, name)
	var missing ssmFailure
	if errors.As(err, &missing) && missing.code == "ResourceNotFoundException" {
		return elbErr("InvalidTarget", "The specified Lambda function does not exist.")
	} else if err != nil {
		return err
	}
	if !lambdaPolicyAllowsELB(policy, region, name, arn) {
		return elbDenied("elasticloadbalancing principal does not have permission to invoke " + function + " from target group " + arn)
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO aws_elb_targets(target_group,function_arn,epoch,state,reason,description,successes,failures,due) VALUES(?,?,?,'initial','Elb.InitialHealthChecking','Initial health checking in progress.',0,0,0)", arn, function, epoch); err != nil {
		return err
	}
	if err = elbBump(ctx, tx, arn, time.Now()); err != nil {
		return err
	}
	return tx.Commit()
}

// DeregisterTarget removes a registered function. An unregistered target
// succeeds, as the official operation documents; there is no draining delay.
func (s *ELBStore) DeregisterTarget(ctx context.Context, region, arn, function string, authorize func(string, string) error) error {
	if err := elbARN(elbTGARNPattern, region, arn, "target group"); err != nil {
		return err
	}
	if _, err := elbFunctionName(region, function); err != nil {
		return err
	}
	if err := authorize("elasticloadbalancing:DeregisterTargets", arn); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, _, err = elbTargetGroupFor(ctx, tx, region, arn); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM aws_elb_targets WHERE target_group=? AND function_arn=?", arn, function)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed > 0 {
		if err = elbBump(ctx, tx, arn, time.Now()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *ELBStore) CreateListener(ctx context.Context, listener ELBListener, authorize func(string, string) error) (ELBListener, error) {
	if err := elbARN(elbLBARNPattern, listener.Region, listener.LoadBalancerARN, "load balancer"); err != nil {
		return ELBListener{}, err
	}
	if listener.Action.Type == "forward" {
		if err := elbARN(elbTGARNPattern, listener.Region, listener.Action.TargetGroupARN, "target group"); err != nil {
			return ELBListener{}, err
		}
	}
	if err := authorize("elasticloadbalancing:CreateListener", listener.LoadBalancerARN); err != nil {
		return ELBListener{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ELBListener{}, err
	}
	defer tx.Rollback()
	lb, err := elbReadLoadBalancer(ctx, tx, "arn=? AND region=?", listener.LoadBalancerARN, listener.Region)
	if errors.Is(err, sql.ErrNoRows) {
		return ELBListener{}, elbErr("LoadBalancerNotFound", "One or more load balancers not found.")
	} else if err != nil {
		return ELBListener{}, err
	}
	existing, err := elbReadListener(ctx, tx, "load_balancer=? AND port=?", lb.ARN, listener.Port)
	if err == nil {
		if existing.Protocol == listener.Protocol && existing.Action == listener.Action {
			return existing, nil
		}
		return ELBListener{}, elbErr("DuplicateListener", "A listener already exists on this port.")
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ELBListener{}, err
	}
	var group any
	wasUsed := true
	if listener.Action.Type == "forward" {
		group = listener.Action.TargetGroupARN
		tg, _, err := elbTargetGroupFor(ctx, tx, listener.Region, listener.Action.TargetGroupARN)
		if err != nil {
			return ELBListener{}, err
		}
		for _, other := range tg.LoadBalancerARNs {
			if other != lb.ARN {
				return ELBListener{}, elbErr("TargetGroupAssociationLimit", "The following target groups cannot be associated with more than one load balancer: "+tg.ARN)
			}
		}
		wasUsed = len(tg.LoadBalancerARNs) > 0
	}
	var count int
	if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM aws_elb_listeners WHERE load_balancer=?", lb.ARN).Scan(&count); err != nil {
		return ELBListener{}, err
	}
	if count >= elbListenerLimit {
		return ELBListener{}, elbErr("TooManyListeners", fmt.Sprintf("This local load balancer supports at most %d listeners.", elbListenerLimit))
	}
	id, err := elbID()
	if err != nil {
		return ELBListener{}, err
	}
	listener.ARN = elbPrefix(listener.Region) + "listener/app/" + lb.Name + "/" + lb.ID + "/" + id
	data, _ := json.Marshal(listener)
	if _, err = tx.ExecContext(ctx, "INSERT INTO aws_elb_listeners(arn,region,load_balancer,port,target_group,document) VALUES(?,?,?,?,?,?)", listener.ARN, listener.Region, lb.ARN, listener.Port, group, string(data)); err != nil {
		return ELBListener{}, err
	}
	if !wasUsed {
		if err = elbBump(ctx, tx, listener.Action.TargetGroupARN, time.Now()); err != nil {
			return ELBListener{}, err
		}
	}
	return listener, tx.Commit()
}

func (s *ELBStore) DeleteListener(ctx context.Context, region, arn string, authorize func(string, string) error) error {
	if err := elbARN(elbLsARNPattern, region, arn, "listener"); err != nil {
		return err
	}
	if err := authorize("elasticloadbalancing:DeleteListener", arn); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	listener, err := elbReadListener(ctx, tx, "arn=? AND region=?", arn, region)
	if errors.Is(err, sql.ErrNoRows) {
		return elbErr("ListenerNotFound", "One or more listeners not found.")
	} else if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, "DELETE FROM aws_elb_listeners WHERE arn=?", arn); err != nil {
		return err
	}
	if listener.Action.Type == "forward" {
		if used, err := elbInUse(ctx, tx, listener.Action.TargetGroupARN); err != nil {
			return err
		} else if !used {
			if err = elbBump(ctx, tx, listener.Action.TargetGroupARN, time.Now()); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// The Describe operations read one consistent snapshot. Explicitly named
// resources must all exist; otherwise the official not-found error is returned.
func (s *ELBStore) DescribeLoadBalancers(ctx context.Context, region string, arns, names []string) ([]ELBLoadBalancer, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var result []ELBLoadBalancer
	switch {
	case len(arns) > 0:
		for _, arn := range arns {
			if err = elbARN(elbLBARNPattern, region, arn, "load balancer"); err != nil {
				return nil, err
			}
		}
		result, err = elbLoadBalancersWhere(ctx, tx, region, arns, "arn")
	case len(names) > 0:
		result, err = elbLoadBalancersWhere(ctx, tx, region, names, "name")
	default:
		var docs []string
		if docs, err = elbStrings(ctx, tx, "SELECT document FROM aws_elb_load_balancers WHERE region=? ORDER BY name", region); err == nil {
			for _, data := range docs {
				var lb ELBLoadBalancer
				if err = json.Unmarshal([]byte(data), &lb); err != nil {
					return nil, err
				}
				result = append(result, lb)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	return result, tx.Commit()
}
func elbLoadBalancersWhere(ctx context.Context, tx *sql.Tx, region string, keys []string, column string) ([]ELBLoadBalancer, error) {
	var result []ELBLoadBalancer
	for _, key := range keys {
		lb, err := elbReadLoadBalancer(ctx, tx, column+"=? AND region=?", key, region)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, elbErr("LoadBalancerNotFound", "One or more load balancers not found.")
		} else if err != nil {
			return nil, err
		}
		result = append(result, lb)
	}
	slices.SortFunc(result, func(a, b ELBLoadBalancer) int { return strings.Compare(a.Name, b.Name) })
	return slices.CompactFunc(result, func(a, b ELBLoadBalancer) bool { return a.ARN == b.ARN }), nil
}

func (s *ELBStore) DescribeTargetGroups(ctx context.Context, region, loadBalancer string, arns, names []string) ([]ELBTargetGroup, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var keys []string
	where := "arn=? AND region=?"
	switch {
	case loadBalancer != "":
		if err = elbARN(elbLBARNPattern, region, loadBalancer, "load balancer"); err != nil {
			return nil, err
		}
		if _, err = elbReadLoadBalancer(ctx, tx, "arn=? AND region=?", loadBalancer, region); errors.Is(err, sql.ErrNoRows) {
			return nil, elbErr("LoadBalancerNotFound", "One or more load balancers not found.")
		} else if err != nil {
			return nil, err
		}
		keys, err = elbStrings(ctx, tx, "SELECT DISTINCT target_group FROM aws_elb_listeners WHERE load_balancer=? AND target_group IS NOT NULL", loadBalancer)
	case len(arns) > 0:
		for _, arn := range arns {
			if err = elbARN(elbTGARNPattern, region, arn, "target group"); err != nil {
				return nil, err
			}
		}
		keys = arns
	case len(names) > 0:
		keys, where = names, "name=? AND region=?"
	default:
		keys, err = elbStrings(ctx, tx, "SELECT arn FROM aws_elb_target_groups WHERE region=?", region)
	}
	if err != nil {
		return nil, err
	}
	var result []ELBTargetGroup
	for _, key := range keys {
		tg, _, err := elbReadTargetGroup(ctx, tx, where, key, region)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, elbErr("TargetGroupNotFound", "One or more target groups not found.")
		} else if err != nil {
			return nil, err
		}
		result = append(result, tg)
	}
	slices.SortFunc(result, func(a, b ELBTargetGroup) int { return strings.Compare(a.Name, b.Name) })
	result = slices.CompactFunc(result, func(a, b ELBTargetGroup) bool { return a.ARN == b.ARN })
	return result, tx.Commit()
}

func (s *ELBStore) DescribeListeners(ctx context.Context, region, loadBalancer string, arns []string) ([]ELBListener, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	keys := arns
	if loadBalancer != "" {
		if err = elbARN(elbLBARNPattern, region, loadBalancer, "load balancer"); err != nil {
			return nil, err
		}
		if _, err = elbReadLoadBalancer(ctx, tx, "arn=? AND region=?", loadBalancer, region); errors.Is(err, sql.ErrNoRows) {
			return nil, elbErr("LoadBalancerNotFound", "One or more load balancers not found.")
		} else if err != nil {
			return nil, err
		}
		if keys, err = elbStrings(ctx, tx, "SELECT arn FROM aws_elb_listeners WHERE load_balancer=?", loadBalancer); err != nil {
			return nil, err
		}
	}
	var result []ELBListener
	for _, key := range keys {
		if err = elbARN(elbLsARNPattern, region, key, "listener"); err != nil {
			return nil, err
		}
		listener, err := elbReadListener(ctx, tx, "arn=? AND region=?", key, region)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, elbErr("ListenerNotFound", "One or more listeners not found.")
		} else if err != nil {
			return nil, err
		}
		result = append(result, listener)
	}
	slices.SortFunc(result, func(a, b ELBListener) int {
		if a.LoadBalancerARN != b.LoadBalancerARN {
			return strings.Compare(a.LoadBalancerARN, b.LoadBalancerARN)
		}
		return a.Port - b.Port
	})
	result = slices.CompactFunc(result, func(a, b ELBListener) bool { return a.ARN == b.ARN })
	return result, tx.Commit()
}

// DescribeTargetHealth reports the recorded health of the registered target and
// the official unused state for any requested target that is not registered.
func (s *ELBStore) DescribeTargetHealth(ctx context.Context, region, arn string, targets []string) ([]ELBTargetHealth, error) {
	if err := elbARN(elbTGARNPattern, region, arn, "target group"); err != nil {
		return nil, err
	}
	for _, target := range targets {
		if _, err := elbFunctionName(region, target); err != nil {
			return nil, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	tg, _, err := elbTargetGroupFor(ctx, tx, region, arn)
	if err != nil {
		return nil, err
	}
	var function, state, reason, description string
	registered := true
	err = tx.QueryRowContext(ctx, "SELECT function_arn,state,reason,description FROM aws_elb_targets WHERE target_group=?", arn).Scan(&function, &state, &reason, &description)
	if errors.Is(err, sql.ErrNoRows) {
		registered = false
	} else if err != nil {
		return nil, err
	}
	health := ELBTargetHealth{Target: function, State: state, Reason: reason, Description: description}
	switch {
	case len(tg.LoadBalancerARNs) == 0:
		health.State, health.Reason, health.Description = "unused", "Target.NotInUse", "Target group is not configured to receive traffic from the load balancer"
	case !tg.Health.Enabled:
		health.State, health.Reason, health.Description = "unavailable", "Target.HealthCheckDisabled", "Health checks are disabled"
	}
	var result []ELBTargetHealth
	if len(targets) == 0 {
		if registered {
			result = append(result, health)
		}
		return result, tx.Commit()
	}
	for _, target := range targets {
		if registered && target == function {
			result = append(result, health)
		} else {
			result = append(result, ELBTargetHealth{Target: target, State: "unused", Reason: "Target.NotRegistered", Description: "Target is not registered to the target group"})
		}
	}
	return result, tx.Commit()
}
