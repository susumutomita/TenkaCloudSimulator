package aws

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// AutoScalingPlansStore owns AWS Auto Scaling scaling plans in provider-owned
// SQLite tables. Its only scalable targets are local PROVISIONED DynamoDB
// tables in the same database. Creating or updating a plan bounds the table's
// provisioned capacity to the instruction limits inside the same transaction.
// Target tracking policies are stored but never evaluated: no metrics exist.
type AutoScalingPlansStore struct {
	db  *sql.DB
	now func() time.Time
}

const (
	maxAutoScalingPlans            = 100
	maxAutoScalingInstructions     = 50
	maxAutoScalingTargetTrackings  = 10
	maxAutoScalingTagFilters       = 50
	maxAutoScalingTagValues        = 20
	maxAutoScalingCapacity         = 40000
	maxAutoScalingPageSize         = 50
	autoScalingPlanVersion         = 1
	autoScalingPolicyType          = "TargetTrackingScaling"
	autoScalingNamespaceDynamoDB   = "dynamodb"
	autoScalingReadDimension       = "dynamodb:table:ReadCapacityUnits"
	autoScalingWriteDimension      = "dynamodb:table:WriteCapacityUnits"
	autoScalingReadUtilization     = "DynamoDBReadCapacityUtilization"
	autoScalingWriteUtilization    = "DynamoDBWriteCapacityUtilization"
	autoScalingPartialMessage      = "Capacity limits were applied to the local DynamoDB table. Target tracking policies are stored but not evaluated because the simulator has no CloudWatch metrics."
	autoScalingActiveMessage       = "Capacity limits were applied to the local DynamoDB table. Dynamic scaling is disabled for this resource."
	autoScalingInactiveMessage     = "The target DynamoDB table no longer exists in this region; this scaling instruction no longer applies."
	autoScalingRecreatedMessage    = "The target DynamoDB table was deleted and recreated; update the scaling plan to apply this instruction to the new table."
	autoScalingPlanProblemsMessage = "One or more scaling instructions are not fully applied. Describe the scaling plan resources for details."
)

type aspFailure struct{ code, message string }

func (e aspFailure) Error() string     { return e.message }
func aspInvalid(message string) error  { return aspFailure{"ValidationException", message} }
func aspNotFound(message string) error { return aspFailure{"ObjectNotFoundException", message} }

// ASPApplicationSource is either a CloudFormation stack ARN or a tag-filter
// set. It identifies the plan's application (one plan per source); the
// simulator does not discover resources from stacks or tags.
type ASPApplicationSource struct {
	CloudFormationStackARN string         `json:"CloudFormationStackARN,omitempty"`
	TagFilters             []ASPTagFilter `json:"TagFilters,omitempty"`
}
type ASPTagFilter struct {
	Key    string   `json:"Key"`
	Values []string `json:"Values,omitempty"`
}
type ASPPredefinedMetric struct {
	PredefinedScalingMetricType string `json:"PredefinedScalingMetricType"`
}
type ASPTargetTracking struct {
	PredefinedScalingMetricSpecification *ASPPredefinedMetric `json:"PredefinedScalingMetricSpecification,omitempty"`
	TargetValue                          float64              `json:"TargetValue"`
	DisableScaleIn                       *bool                `json:"DisableScaleIn,omitempty"`
	ScaleOutCooldown                     *int64               `json:"ScaleOutCooldown,omitempty"`
	ScaleInCooldown                      *int64               `json:"ScaleInCooldown,omitempty"`
}
type ASPInstruction struct {
	ServiceNamespace             string              `json:"ServiceNamespace"`
	ResourceID                   string              `json:"ResourceId"`
	ScalableDimension            string              `json:"ScalableDimension"`
	MinCapacity                  int64               `json:"MinCapacity"`
	MaxCapacity                  int64               `json:"MaxCapacity"`
	TargetTrackingConfigurations []ASPTargetTracking `json:"TargetTrackingConfigurations"`
	ScalingPolicyUpdateBehavior  string              `json:"ScalingPolicyUpdateBehavior,omitempty"`
	DisableDynamicScaling        *bool               `json:"DisableDynamicScaling,omitempty"`
}
type ASPPlan struct {
	ScalingPlanName     string               `json:"ScalingPlanName"`
	ScalingPlanVersion  int64                `json:"ScalingPlanVersion"`
	ApplicationSource   ASPApplicationSource `json:"ApplicationSource"`
	ScalingInstructions []ASPInstruction     `json:"ScalingInstructions"`
	StatusCode          string               `json:"StatusCode"`
	StatusMessage       string               `json:"StatusMessage,omitempty"`
	StatusStartTime     float64              `json:"StatusStartTime"`
	CreationTime        float64              `json:"CreationTime"`
}
type ASPPolicy struct {
	PolicyName                  string             `json:"PolicyName"`
	PolicyType                  string             `json:"PolicyType"`
	TargetTrackingConfiguration *ASPTargetTracking `json:"TargetTrackingConfiguration,omitempty"`
}
type ASPResource struct {
	ScalingPlanName      string      `json:"ScalingPlanName"`
	ScalingPlanVersion   int64       `json:"ScalingPlanVersion"`
	ServiceNamespace     string      `json:"ServiceNamespace"`
	ResourceID           string      `json:"ResourceId"`
	ScalableDimension    string      `json:"ScalableDimension"`
	ScalingPolicies      []ASPPolicy `json:"ScalingPolicies,omitempty"`
	ScalingStatusCode    string      `json:"ScalingStatusCode"`
	ScalingStatusMessage string      `json:"ScalingStatusMessage,omitempty"`
}

// ASPPlanQuery selects plans by exact names or exact application sources.
type ASPPlanQuery struct {
	Names   []string
	Sources []ASPApplicationSource
	Token   string
	Limit   int
}

// NewAutoScalingPlansStore requires the DynamoDB store of the same database:
// scaling targets are DynamoDB table rows updated in one SQLite transaction.
func NewAutoScalingPlansStore(db *sql.DB, dynamo *DynamoDBStore) (*AutoScalingPlansStore, error) {
	if db == nil || dynamo == nil || dynamo.db != db {
		return nil, errors.New("Auto Scaling plans require the DynamoDB store of the same database")
	}
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS aws_autoscaling_plans(region TEXT NOT NULL,name TEXT NOT NULL,source_key TEXT NOT NULL,source TEXT NOT NULL,created INTEGER NOT NULL,status_start INTEGER NOT NULL,generation TEXT NOT NULL,PRIMARY KEY(region,name),UNIQUE(region,source_key));
 CREATE TABLE IF NOT EXISTS aws_autoscaling_plan_resources(region TEXT NOT NULL,plan TEXT NOT NULL,ordinal INTEGER NOT NULL,namespace TEXT NOT NULL,resource_id TEXT NOT NULL,dimension TEXT NOT NULL,instruction TEXT NOT NULL,table_id TEXT NOT NULL,PRIMARY KEY(region,plan,ordinal),UNIQUE(region,namespace,resource_id,dimension),FOREIGN KEY(region,plan) REFERENCES aws_autoscaling_plans(region,name) ON DELETE CASCADE);`)
	if err != nil {
		return nil, err
	}
	// Refuse orphaned resources without migrating, deleting or adopting rows.
	var orphan int
	err = db.QueryRow(`SELECT 1 FROM aws_autoscaling_plan_resources r LEFT JOIN aws_autoscaling_plans p ON p.region=r.region AND p.name=r.plan WHERE p.name IS NULL LIMIT 1`).Scan(&orphan)
	if err == nil {
		return nil, errors.New("Auto Scaling plan state contains orphaned resources; refusing startup without modifying existing data")
	}
	if err != sql.ErrNoRows {
		return nil, err
	}
	return &AutoScalingPlansStore{db: db, now: time.Now}, nil
}

// aspSourceKey canonicalizes one application source for the one-plan-per-source
// rule. Tag filters are order-insensitive; validation already rejected duplicates.
func aspSourceKey(source ASPApplicationSource) string {
	if source.CloudFormationStackARN != "" {
		return "stack\x00" + source.CloudFormationStackARN
	}
	filters := make([]ASPTagFilter, len(source.TagFilters))
	for i, filter := range source.TagFilters {
		values := slices.Clone(filter.Values)
		slices.Sort(values)
		filters[i] = ASPTagFilter{Key: filter.Key, Values: values}
	}
	slices.SortFunc(filters, func(a, b ASPTagFilter) int { return strings.Compare(a.Key, b.Key) })
	data, _ := json.Marshal(filters)
	return "tags\x00" + string(data)
}

func aspTableName(resourceID string) string { return strings.TrimPrefix(resourceID, "table/") }

func aspMillis(t time.Time) int64 { return t.UnixMilli() }
func aspSeconds(ms int64) float64 { return float64(ms) / 1000 }

// applyInstructions validates each target against current DynamoDB state, then
// bounds its provisioned capacity and records the table generation. It runs
// inside the caller's transaction; any failure rolls back every write.
func (s *AutoScalingPlansStore) applyInstructions(tx *sql.Tx, region, plan string, instructions []ASPInstruction) error {
	if _, err := tx.Exec(`DELETE FROM aws_autoscaling_plan_resources WHERE region=? AND plan=?`, region, plan); err != nil {
		return err
	}
	for ordinal, instruction := range instructions {
		name := aspTableName(instruction.ResourceID)
		var owner string
		err := tx.QueryRow(`SELECT plan FROM aws_autoscaling_plan_resources WHERE region=? AND namespace=? AND resource_id=? AND dimension=?`, region, instruction.ServiceNamespace, instruction.ResourceID, instruction.ScalableDimension).Scan(&owner)
		if err == nil {
			return aspInvalid(fmt.Sprintf("%s %s is already managed by scaling plan %s.", instruction.ResourceID, instruction.ScalableDimension, owner))
		}
		if err != sql.ErrNoRows {
			return err
		}
		table, err := readDynamoTable(tx, region, name)
		var missing dynamoFailure
		if errors.As(err, &missing) && missing.code == "ResourceNotFoundException" {
			return aspInvalid(fmt.Sprintf("Scaling target %s was not found in %s.", instruction.ResourceID, region))
		}
		if err != nil {
			return err
		}
		if table.BillingMode != "PROVISIONED" {
			return aspInvalid(fmt.Sprintf("Scaling target %s uses on-demand capacity; only PROVISIONED tables have capacity units to scale.", instruction.ResourceID))
		}
		current, update := table.ReadUnits, `UPDATE aws_dynamodb_tables SET read_units=? WHERE region=? AND name=? AND table_id=?`
		if instruction.ScalableDimension == autoScalingWriteDimension {
			current, update = table.WriteUnits, `UPDATE aws_dynamodb_tables SET write_units=? WHERE region=? AND name=? AND table_id=?`
		}
		bounded := min(max(current, instruction.MinCapacity), instruction.MaxCapacity)
		if bounded != current {
			if err = dynamoExecExact(tx, 1, update, bounded, region, name, table.TableID); err != nil {
				return err
			}
		}
		document, err := json.Marshal(instruction)
		if err != nil {
			return err
		}
		if err = dynamoExecExact(tx, 1, `INSERT INTO aws_autoscaling_plan_resources VALUES(?,?,?,?,?,?,?,?)`, region, plan, ordinal, instruction.ServiceNamespace, instruction.ResourceID, instruction.ScalableDimension, string(document), table.TableID); err != nil {
			return err
		}
	}
	return nil
}

func (s *AutoScalingPlansStore) sourceOwner(tx *sql.Tx, region, key, except string) error {
	var owner string
	err := tx.QueryRow(`SELECT name FROM aws_autoscaling_plans WHERE region=? AND source_key=? AND name<>?`, region, key, except).Scan(&owner)
	if err == nil {
		return aspInvalid("An application source can have only one scaling plan; scaling plan " + owner + " already uses it.")
	}
	if err != sql.ErrNoRows {
		return err
	}
	return nil
}

func (s *AutoScalingPlansStore) Create(ctx context.Context, region, name string, source ASPApplicationSource, instructions []ASPInstruction) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err = tx.QueryRow(`SELECT count(*) FROM aws_autoscaling_plans`).Scan(&count); err != nil {
		return err
	}
	var existing string
	err = tx.QueryRow(`SELECT name FROM aws_autoscaling_plans WHERE region=? AND name=?`, region, name).Scan(&existing)
	if err == nil {
		return aspInvalid("Scaling plan " + name + " already exists.")
	}
	if err != sql.ErrNoRows {
		return err
	}
	if count >= maxAutoScalingPlans {
		return aspFailure{"LimitExceededException", fmt.Sprintf("The simulator limit of %d scaling plans per database is reached.", maxAutoScalingPlans)}
	}
	key := aspSourceKey(source)
	if err = s.sourceOwner(tx, region, key, ""); err != nil {
		return err
	}
	document, err := json.Marshal(source)
	if err != nil {
		return err
	}
	// A random generation distinguishes a recreated plan of the same name even
	// within one clock tick; continuation tokens bind to it.
	generation := make([]byte, 16)
	if _, err = rand.Read(generation); err != nil {
		return err
	}
	now := aspMillis(s.now())
	if err = dynamoExecExact(tx, 1, `INSERT INTO aws_autoscaling_plans VALUES(?,?,?,?,?,?,?)`, region, name, key, string(document), now, now, hex.EncodeToString(generation)); err != nil {
		return err
	}
	if err = s.applyInstructions(tx, region, name, instructions); err != nil {
		return err
	}
	return tx.Commit()
}

// Update replaces the supplied parts. A nil source or nil instruction list keeps
// the stored value. Replaced instructions re-apply capacity limits; removed
// targets keep their current capacity, as plan deletion does.
func (s *AutoScalingPlansStore) Update(ctx context.Context, region, name string, source *ASPApplicationSource, instructions []ASPInstruction) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var existing string
	err = tx.QueryRow(`SELECT name FROM aws_autoscaling_plans WHERE region=? AND name=?`, region, name).Scan(&existing)
	if err == sql.ErrNoRows {
		return aspNotFound("Scaling plan " + name + " does not exist.")
	}
	if err != nil {
		return err
	}
	if source != nil {
		key := aspSourceKey(*source)
		if err = s.sourceOwner(tx, region, key, name); err != nil {
			return err
		}
		document, err := json.Marshal(*source)
		if err != nil {
			return err
		}
		if err = dynamoExecExact(tx, 1, `UPDATE aws_autoscaling_plans SET source_key=?,source=? WHERE region=? AND name=?`, key, string(document), region, name); err != nil {
			return err
		}
	}
	if instructions != nil {
		if err = s.applyInstructions(tx, region, name, instructions); err != nil {
			return err
		}
	}
	if source != nil || instructions != nil {
		if err = dynamoExecExact(tx, 1, `UPDATE aws_autoscaling_plans SET status_start=? WHERE region=? AND name=?`, aspMillis(s.now()), region, name); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Delete removes the plan and its instructions. Table capacity is not reverted.
func (s *AutoScalingPlansStore) Delete(ctx context.Context, region, name string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`DELETE FROM aws_autoscaling_plan_resources WHERE region=? AND plan=?`, region, name); err != nil {
		return err
	}
	result, err := tx.Exec(`DELETE FROM aws_autoscaling_plans WHERE region=? AND name=?`, region, name)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return aspNotFound("Scaling plan " + name + " does not exist.")
	}
	if affected != 1 {
		return errors.New("Auto Scaling plan deletion affected an unexpected number of rows")
	}
	return tx.Commit()
}

type aspStoredPlan struct {
	name, source string
	created      int64
	statusStart  int64
}

type aspQuerier interface {
	QueryRow(string, ...any) *sql.Row
	Query(string, ...any) (*sql.Rows, error)
}

// resources reads one plan's instructions in order and derives each status
// from the current DynamoDB table generation.
func (s *AutoScalingPlansStore) resources(q aspQuerier, region, plan string) ([]ASPInstruction, []ASPResource, error) {
	rows, err := q.Query(`SELECT namespace,resource_id,dimension,instruction,table_id FROM aws_autoscaling_plan_resources WHERE region=? AND plan=? ORDER BY ordinal`, region, plan)
	if err != nil {
		return nil, nil, err
	}
	type stored struct {
		instruction ASPInstruction
		tableID     string
	}
	var items []stored
	for rows.Next() {
		var namespace, resourceID, dimension, document, tableID string
		if err = rows.Scan(&namespace, &resourceID, &dimension, &document, &tableID); err != nil {
			rows.Close()
			return nil, nil, err
		}
		var instruction ASPInstruction
		if err = json.Unmarshal([]byte(document), &instruction); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if instruction.ServiceNamespace != namespace || instruction.ResourceID != resourceID || instruction.ScalableDimension != dimension {
			rows.Close()
			return nil, nil, errors.New("stored Auto Scaling instruction does not match its target")
		}
		items = append(items, stored{instruction, tableID})
	}
	if err = rows.Close(); err != nil {
		return nil, nil, err
	}
	if err = rows.Err(); err != nil {
		return nil, nil, err
	}
	instructions := make([]ASPInstruction, 0, len(items))
	resources := make([]ASPResource, 0, len(items))
	for _, item := range items {
		instruction := item.instruction
		instructions = append(instructions, instruction)
		resource := ASPResource{ScalingPlanName: plan, ScalingPlanVersion: autoScalingPlanVersion, ServiceNamespace: instruction.ServiceNamespace, ResourceID: instruction.ResourceID, ScalableDimension: instruction.ScalableDimension}
		var tableID string
		err = q.QueryRow(`SELECT table_id FROM aws_dynamodb_tables WHERE region=? AND name=?`, region, aspTableName(instruction.ResourceID)).Scan(&tableID)
		if err != nil && err != sql.ErrNoRows {
			return nil, nil, err
		}
		switch {
		case err == sql.ErrNoRows:
			resource.ScalingStatusCode, resource.ScalingStatusMessage = "Inactive", autoScalingInactiveMessage
		case tableID != item.tableID:
			resource.ScalingStatusCode, resource.ScalingStatusMessage = "Inactive", autoScalingRecreatedMessage
		case instruction.DisableDynamicScaling != nil && *instruction.DisableDynamicScaling:
			resource.ScalingStatusCode, resource.ScalingStatusMessage = "Active", autoScalingActiveMessage
		default:
			resource.ScalingStatusCode, resource.ScalingStatusMessage = "PartiallyActive", autoScalingPartialMessage
			for i := range instruction.TargetTrackingConfigurations {
				configuration := instruction.TargetTrackingConfigurations[i]
				if configuration.PredefinedScalingMetricSpecification == nil {
					return nil, nil, errors.New("stored Auto Scaling target tracking configuration has no metric")
				}
				resource.ScalingPolicies = append(resource.ScalingPolicies, ASPPolicy{
					PolicyName:                  "ScalingPlan-" + plan + "-" + configuration.PredefinedScalingMetricSpecification.PredefinedScalingMetricType,
					PolicyType:                  autoScalingPolicyType,
					TargetTrackingConfiguration: &configuration,
				})
			}
		}
		resources = append(resources, resource)
	}
	return instructions, resources, nil
}

func (s *AutoScalingPlansStore) describe(q aspQuerier, region string, stored aspStoredPlan) (ASPPlan, error) {
	var source ASPApplicationSource
	if err := json.Unmarshal([]byte(stored.source), &source); err != nil {
		return ASPPlan{}, err
	}
	instructions, resources, err := s.resources(q, region, stored.name)
	if err != nil {
		return ASPPlan{}, err
	}
	plan := ASPPlan{ScalingPlanName: stored.name, ScalingPlanVersion: autoScalingPlanVersion, ApplicationSource: source, ScalingInstructions: instructions,
		StatusCode: "Active", StatusStartTime: aspSeconds(stored.statusStart), CreationTime: aspSeconds(stored.created)}
	for _, resource := range resources {
		if resource.ScalingStatusCode != "Active" {
			plan.StatusCode, plan.StatusMessage = "ActiveWithProblems", autoScalingPlanProblemsMessage
		}
	}
	return plan, nil
}

// aspToken encodes a live continuation position bound to its query. Tokens are
// standard search bounds, not authority: every page is reauthorized.
func aspToken(after, binding string) string {
	data, _ := json.Marshal(map[string]string{"a": after, "b": binding})
	return "v1." + base64.RawURLEncoding.EncodeToString(data)
}
func aspReadToken(token, binding string) (string, error) {
	invalid := aspFailure{"InvalidNextTokenException", "The NextToken is invalid for this request."}
	encoded, ok := strings.CutPrefix(token, "v1.")
	if !ok || len(token) > 1024 {
		return "", invalid
	}
	data, err := base64.RawURLEncoding.DecodeString(encoded)
	var decoded map[string]string
	if err != nil || json.Unmarshal(data, &decoded) != nil || len(decoded) != 2 || decoded["b"] != binding {
		return "", invalid
	}
	after, ok := decoded["a"]
	if !ok {
		return "", invalid
	}
	return after, nil
}
func aspBinding(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:16])
}

func aspLimit(limit int) error {
	if limit < 1 || limit > maxAutoScalingPageSize {
		return aspInvalid(fmt.Sprintf("MaxResults must be an integer from 1 to %d.", maxAutoScalingPageSize))
	}
	return nil
}

func (s *AutoScalingPlansStore) Plans(ctx context.Context, region string, query ASPPlanQuery) ([]ASPPlan, string, error) {
	if err := aspLimit(query.Limit); err != nil {
		return nil, "", err
	}
	keys := map[string]bool{}
	filter := []string{region}
	for _, name := range query.Names {
		filter = append(filter, "name", name)
	}
	for _, source := range query.Sources {
		key := aspSourceKey(source)
		keys[key] = true
		filter = append(filter, "source", key)
	}
	names := map[string]bool{}
	for _, name := range query.Names {
		names[name] = true
	}
	binding := aspBinding(filter...)
	after := ""
	if query.Token != "" {
		var err error
		if after, err = aspReadToken(query.Token, binding); err != nil {
			return nil, "", err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT name,source_key,source,created,status_start FROM aws_autoscaling_plans WHERE region=? AND name>? ORDER BY name`, region, after)
	if err != nil {
		return nil, "", err
	}
	var selected []aspStoredPlan
	for rows.Next() {
		var item aspStoredPlan
		var key string
		if err = rows.Scan(&item.name, &key, &item.source, &item.created, &item.statusStart); err != nil {
			rows.Close()
			return nil, "", err
		}
		if len(names) > 0 && !names[item.name] || len(keys) > 0 && !keys[key] {
			continue
		}
		selected = append(selected, item)
	}
	if err = rows.Close(); err != nil {
		return nil, "", err
	}
	if err = rows.Err(); err != nil {
		return nil, "", err
	}
	next := ""
	if len(selected) > query.Limit {
		selected = selected[:query.Limit]
		next = aspToken(selected[len(selected)-1].name, binding)
	}
	plans := make([]ASPPlan, 0, len(selected))
	for _, item := range selected {
		plan, err := s.describe(tx, region, item)
		if err != nil {
			return nil, "", err
		}
		plans = append(plans, plan)
	}
	return plans, next, tx.Commit()
}

func (s *AutoScalingPlansStore) Resources(ctx context.Context, region, name, token string, limit int) ([]ASPResource, string, error) {
	if err := aspLimit(limit); err != nil {
		return nil, "", err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, "", err
	}
	defer tx.Rollback()
	var generation string
	err = tx.QueryRow(`SELECT generation FROM aws_autoscaling_plans WHERE region=? AND name=?`, region, name).Scan(&generation)
	if err == sql.ErrNoRows {
		return nil, "", aspInvalid("Scaling plan " + name + " does not exist.")
	}
	if err != nil {
		return nil, "", err
	}
	// A recreated plan with the same name is a different generation.
	binding := aspBinding(region, name, generation)
	start := 0
	if token != "" {
		after, err := aspReadToken(token, binding)
		if err != nil {
			return nil, "", err
		}
		if _, err = fmt.Sscanf(after, "%d", &start); err != nil || start < 1 || fmt.Sprint(start) != after {
			return nil, "", aspFailure{"InvalidNextTokenException", "The NextToken is invalid for this request."}
		}
	}
	_, resources, err := s.resources(tx, region, name)
	if err != nil {
		return nil, "", err
	}
	if start > len(resources) {
		start = len(resources)
	}
	resources = resources[start:]
	next := ""
	if len(resources) > limit {
		resources = resources[:limit]
		next = aspToken(fmt.Sprint(start+limit), binding)
	}
	return resources, next, tx.Commit()
}
