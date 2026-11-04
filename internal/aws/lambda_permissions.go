package aws

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

type LambdaPermissionStore struct{ db *sql.DB }
type LambdaResourceStatement struct {
	Sid       string                       `json:"Sid"`
	Effect    string                       `json:"Effect"`
	Principal map[string]string            `json:"Principal"`
	Action    string                       `json:"Action"`
	Resource  string                       `json:"Resource"`
	Condition map[string]map[string]string `json:"Condition"`
}
type LambdaResourcePolicy struct {
	Version   string                    `json:"Version"`
	ID        string                    `json:"Id"`
	Statement []LambdaResourceStatement `json:"Statement"`
}

func NewLambdaPermissionStore(db *sql.DB) (*LambdaPermissionStore, error) {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS aws_lambda_resource_policies(region TEXT NOT NULL,function_name TEXT NOT NULL,revision TEXT NOT NULL,document TEXT NOT NULL,PRIMARY KEY(region,function_name),FOREIGN KEY(region,function_name) REFERENCES aws_lambda_functions(region,name) ON DELETE CASCADE);`)
	return &LambdaPermissionStore{db: db}, err
}

var lambdaStatementID = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,100}$`)
var apiLocalID = regexp.MustCompile(`^[a-f0-9]{10}$`)
var apiSourceStage = regexp.MustCompile(`^[a-zA-Z0-9_*?-]{1,128}$`)
var apiSourceMethod = regexp.MustCompile(`^[A-Z*?]{1,16}$`)

func localAPISource(region, source string) bool {
	prefix := "arn:aws:execute-api:" + region + ":000000000000:"
	if !strings.HasPrefix(source, prefix) || len(source) > 1024 || !utf8.ValidString(source) {
		return false
	}
	fields := strings.SplitN(strings.TrimPrefix(source, prefix), "/", 4)
	if len(fields) != 4 || !apiLocalID.MatchString(fields[0]) || !apiSourceStage.MatchString(fields[1]) || !apiSourceMethod.MatchString(fields[2]) {
		return false
	}
	for _, r := range fields[3] {
		if r < 32 || r == 127 {
			return false
		}
	}
	return true
}
func lambdaPolicyRead(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, region, name string) (LambdaResourcePolicy, string, error) {
	var exists int
	if err := query.QueryRowContext(ctx, "SELECT 1 FROM aws_lambda_functions WHERE region=? AND name=?", region, name).Scan(&exists); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return LambdaResourcePolicy{}, "", apiFailure("ResourceNotFoundException", "Function not found.")
		}
		return LambdaResourcePolicy{}, "", err
	}
	var revision, data string
	err := query.QueryRowContext(ctx, "SELECT revision,document FROM aws_lambda_resource_policies WHERE region=? AND function_name=?", region, name).Scan(&revision, &data)
	if errors.Is(err, sql.ErrNoRows) {
		return LambdaResourcePolicy{Version: "2012-10-17", ID: "default", Statement: []LambdaResourceStatement{}}, "", nil
	}
	if err != nil {
		return LambdaResourcePolicy{}, "", err
	}
	var policy LambdaResourcePolicy
	err = json.Unmarshal([]byte(data), &policy)
	return policy, revision, err
}
func (s *LambdaPermissionStore) Policy(ctx context.Context, region, name string) (LambdaResourcePolicy, string, error) {
	return lambdaPolicyRead(ctx, s.db, region, name)
}
func (s *LambdaPermissionStore) Add(ctx context.Context, region, name, sid, principal, action, source, revision string) (LambdaResourceStatement, error) {
	if !lambdaStatementID.MatchString(sid) || principal != "apigateway.amazonaws.com" || action != "lambda:InvokeFunction" || !localAPISource(region, source) {
		return LambdaResourceStatement{}, apiFailure("InvalidParameterValueException", "Use a bounded local API Gateway SourceArn and InvokeFunction permission.")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return LambdaResourceStatement{}, err
	}
	defer tx.Rollback()
	policy, current, err := lambdaPolicyRead(ctx, tx, region, name)
	if err != nil {
		return LambdaResourceStatement{}, err
	}
	if revision != "" && revision != current {
		return LambdaResourceStatement{}, apiFailure("PreconditionFailedException", "Resource policy revision changed.")
	}
	for _, statement := range policy.Statement {
		if statement.Sid == sid {
			return LambdaResourceStatement{}, apiFailure("ResourceConflictException", "Statement already exists.")
		}
	}
	if len(policy.Statement) >= 20 {
		return LambdaResourceStatement{}, apiFailure("PolicyLengthExceededException", "Local resource policy supports at most20statements.")
	}
	statement := LambdaResourceStatement{Sid: sid, Effect: "Allow", Principal: map[string]string{"Service": principal}, Action: action, Resource: lambdaARN(region, name), Condition: map[string]map[string]string{"ArnLike": {"AWS:SourceArn": source}}}
	policy.Statement = append(policy.Statement, statement)
	data, _ := json.Marshal(policy)
	if len(data) > 20<<10 {
		return LambdaResourceStatement{}, apiFailure("PolicyLengthExceededException", "Resource policy exceeds20KiB.")
	}
	next, err := s3NewID()
	if err != nil {
		return LambdaResourceStatement{}, err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO aws_lambda_resource_policies(region,function_name,revision,document) VALUES(?,?,?,?) ON CONFLICT(region,function_name) DO UPDATE SET revision=excluded.revision,document=excluded.document", region, name, next, string(data))
	if err != nil {
		return LambdaResourceStatement{}, err
	}
	return statement, tx.Commit()
}
func (s *LambdaPermissionStore) AllowsAPI(ctx context.Context, region, name, source string) (bool, error) {
	if !localAPISource(region, source) || strings.ContainsAny(source, "*?") {
		return false, nil
	}
	policy, _, err := s.Policy(ctx, region, name)
	if err != nil {
		return false, err
	}
	return lambdaPolicyAllowsAPI(policy, region, name, source), nil
}
func lambdaPolicyAllowsAPI(policy LambdaResourcePolicy, region, name, source string) bool {
	if !localAPISource(region, source) || strings.ContainsAny(source, "*?") {
		return false
	}
	for _, statement := range policy.Statement {
		if statement.Effect != "Allow" || statement.Principal["Service"] != "apigateway.amazonaws.com" || statement.Action != "lambda:InvokeFunction" || statement.Resource != lambdaARN(region, name) {
			continue
		}
		pattern := statement.Condition["ArnLike"]["AWS:SourceArn"]
		if !localAPISource(region, pattern) {
			continue
		}
		if core.Evaluate(core.Policy{Version: "2012-10-17", Statement: core.Statements{{Effect: "Allow", Action: core.StringList{"lambda:InvokeFunction"}, Resource: core.StringList{pattern}}}}, "lambda:InvokeFunction", source) == "allowed" {
			return true
		}
	}
	return false
}

func (s *LambdaPermissionStore) Remove(ctx context.Context, region, name, sid, revision string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	policy, current, err := lambdaPolicyRead(ctx, tx, region, name)
	if err != nil {
		return err
	}
	if revision != "" && revision != current {
		return apiFailure("PreconditionFailedException", "Resource policy revision changed.")
	}
	index := -1
	for i, statement := range policy.Statement {
		if statement.Sid == sid {
			index = i
			break
		}
	}
	if index < 0 {
		return apiFailure("ResourceNotFoundException", "Statement not found.")
	}
	policy.Statement = append(policy.Statement[:index], policy.Statement[index+1:]...)
	next, err := s3NewID()
	if err != nil {
		return err
	}
	data, _ := json.Marshal(policy)
	if _, err = tx.ExecContext(ctx, "UPDATE aws_lambda_resource_policies SET revision=?,document=? WHERE region=? AND function_name=?", next, string(data), region, name); err != nil {
		return err
	}
	return tx.Commit()
}
