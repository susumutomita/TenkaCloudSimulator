// Package codestarnotifications owns the local team's notification rules and
// delivery records. The Git and messaging providers keep their own resources.
package codestarnotifications

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	BranchCreated     = "codecommit-repository-branches-and-tags-created"
	BranchUpdated     = "codecommit-repository-branches-and-tags-updated"
	MaxRulesPerSource = 10
	maxTeamRules      = 256
)

type Fault struct{ Code, Message string }

func (f Fault) Error() string      { return f.Message }
func invalid(message string) error { return Fault{"ValidationException", message} }
func missing() error {
	return Fault{"ResourceNotFoundException", "The notification rule was not found in this region."}
}
func denied() error {
	return Fault{"AccessDeniedException", "Permission denied for this notification operation."}
}

// Grant returns the current physical generation and scoped service grant for
// an existing native Standard SNS topic in this team's transaction.
type Grant func(context.Context, *sql.Tx, string) (int64, bool, error)
type Decision func(action, resource string) string

type Store struct {
	db       *sql.DB
	grant    Grant
	now      func() time.Time
	tokenKey [32]byte
}

const schema = `
CREATE TABLE IF NOT EXISTS aws_notification_rules (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, arn TEXT UNIQUE NOT NULL,
 region TEXT NOT NULL, name TEXT NOT NULL, owner TEXT NOT NULL, source TEXT NOT NULL
 REFERENCES aws_catalog_resources(arn) ON DELETE CASCADE, repository_id TEXT NOT NULL,
 created_ms INTEGER NOT NULL, updated_ms INTEGER NOT NULL, generation INTEGER NOT NULL,
 enabled INTEGER NOT NULL, events TEXT NOT NULL, token TEXT, fingerprint TEXT NOT NULL,
 UNIQUE(region,name), UNIQUE(region,owner,token)
);
CREATE TABLE IF NOT EXISTS aws_notification_owners (
 rule_arn TEXT PRIMARY KEY REFERENCES aws_notification_rules(arn) ON DELETE CASCADE,
 principal TEXT NOT NULL REFERENCES identity_policies(name) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS aws_notification_targets (
 rule_arn TEXT NOT NULL REFERENCES aws_notification_rules(arn) ON DELETE CASCADE,
 topic_arn TEXT NOT NULL REFERENCES aws_sns_topics(arn) ON DELETE CASCADE,
 generation INTEGER NOT NULL, PRIMARY KEY(rule_arn,topic_arn)
);
CREATE TABLE IF NOT EXISTS aws_notification_queue_instances (
 generation INTEGER PRIMARY KEY AUTOINCREMENT, region TEXT NOT NULL, queue TEXT NOT NULL,
 UNIQUE(region,queue), FOREIGN KEY(region,queue) REFERENCES aws_sqs_queues(region,name) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS aws_notification_subscriptions (
 subscription_arn TEXT PRIMARY KEY REFERENCES aws_sns_subscriptions(arn) ON DELETE CASCADE,
 queue_generation INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS aws_notification_outbox (
 seq INTEGER PRIMARY KEY AUTOINCREMENT, rule_arn TEXT NOT NULL, rule_generation INTEGER NOT NULL,
 topic_arn TEXT NOT NULL, topic_generation INTEGER NOT NULL, event_id TEXT NOT NULL,
 envelope TEXT NOT NULL, topology TEXT NOT NULL, retry_ms INTEGER NOT NULL DEFAULT 0,
 UNIQUE(rule_arn,topic_arn,event_id),
 FOREIGN KEY(rule_arn,topic_arn) REFERENCES aws_notification_targets(rule_arn,topic_arn) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS aws_notification_outbox_ready ON aws_notification_outbox(retry_ms,seq);
CREATE TABLE IF NOT EXISTS aws_notification_deliveries (
 rule_arn TEXT NOT NULL, topic_arn TEXT NOT NULL, event_id TEXT NOT NULL,
 message_id TEXT NOT NULL UNIQUE REFERENCES aws_sns_messages(id) ON DELETE CASCADE,
 PRIMARY KEY(rule_arn,topic_arn,event_id)
);`

func New(db *sql.DB, grant Grant) (*Store, error) {
	if db == nil || grant == nil {
		return nil, errors.New("notification rules require the team database and native SNS grant resolver")
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(schema); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s := &Store{db: db, grant: grant, now: time.Now}
	rand.Read(s.tokenKey[:])
	return s, nil
}

type Target struct {
	Type    string `json:"TargetType"`
	Address string `json:"TargetAddress"`
}
type Filter struct {
	Name  string `json:"Name"`
	Value string `json:"Value"`
}
type Input struct {
	ARN        string    `json:"Arn"`
	Name       *string   `json:"Name"`
	Events     *[]string `json:"EventTypeIds"`
	Source     string    `json:"Resource"`
	Targets    *[]Target `json:"Targets"`
	DetailType *string   `json:"DetailType"`
	Token      string    `json:"ClientRequestToken"`
	Status     *string   `json:"Status"`
	Filters    []Filter  `json:"Filters"`
	NextToken  string    `json:"NextToken"`
	MaxResults *int      `json:"MaxResults"`
}

type Rule struct {
	Seq                                            int64
	ARN, Region, Name, Owner, Source, RepositoryID string
	Created, Updated, Generation                   int64
	Enabled                                        bool
	Events                                         []string
}

var (
	validName   = regexp.MustCompile(`^[A-Za-z0-9_ -]{1,64}$`)
	validToken  = regexp.MustCompile(`^[A-Za-z0-9_:/-]{1,256}$`)
	validRuleID = regexp.MustCompile(`^[a-f0-9]{40}$`)
)

func prefix(service, region string) string {
	return "arn:aws:" + service + ":" + region + ":000000000000:"
}
func validRuleARN(arn, region string) bool {
	id, ok := strings.CutPrefix(arn, prefix("codestar-notifications", region)+"notificationrule/")
	return ok && validRuleID.MatchString(id)
}
func newARN(region string) string {
	var id [20]byte
	rand.Read(id[:])
	return prefix("codestar-notifications", region) + fmt.Sprintf("notificationrule/%x", id[:])
}

func validateChanges(in Input) error {
	if in.Name != nil && !validName.MatchString(*in.Name) {
		return invalid("A rule name must contain 1 to 64 letters, digits, spaces, underscores or hyphens.")
	}
	if in.DetailType != nil && *in.DetailType != "BASIC" {
		return invalid("Select BASIC for local branch notifications.")
	}
	if in.Status != nil && *in.Status != "ENABLED" && *in.Status != "DISABLED" {
		return invalid("Status must be ENABLED or DISABLED.")
	}
	if in.Events != nil {
		if len(*in.Events) < 1 || len(*in.Events) > 2 {
			return invalid("Select one or both local branch event types.")
		}
		seen := map[string]bool{}
		for _, event := range *in.Events {
			if (event != BranchCreated && event != BranchUpdated) || seen[event] {
				return invalid("Unknown or duplicate branch event type.")
			}
			seen[event] = true
		}
	}
	if in.Targets != nil {
		if len(*in.Targets) > 10 {
			return invalid("At most ten targets may be supplied.")
		}
		seen := map[string]bool{}
		for _, t := range *in.Targets {
			if t.Type != "SNS" || t.Address == "" || seen[t.Address] {
				return invalid("Select distinct SNS topic targets.")
			}
			seen[t.Address] = true
		}
	}
	return nil
}

func normalizedEvents(events []string) string {
	e := slices.Clone(events)
	slices.Sort(e)
	return strings.Join(e, ",")
}

func selectRule(ctx context.Context, tx *sql.Tx, region, arn string) (Rule, error) {
	var r Rule
	var events string
	err := tx.QueryRowContext(ctx, `SELECT seq,arn,region,name,owner,source,repository_id,created_ms,updated_ms,generation,enabled,events
		FROM aws_notification_rules WHERE region=? AND arn=?`, region, arn).Scan(&r.Seq, &r.ARN, &r.Region, &r.Name, &r.Owner, &r.Source, &r.RepositoryID, &r.Created, &r.Updated, &r.Generation, &r.Enabled, &events)
	if errors.Is(err, sql.ErrNoRows) {
		return r, missing()
	}
	r.Events = strings.Split(events, ",")
	return r, err
}

func checkedExec(ctx context.Context, tx *sql.Tx, expected int64, query string, args ...any) error {
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != expected {
		return fmt.Errorf("notification storage changed %d rows, expected %d", n, expected)
	}
	return nil
}

func requireCount(ctx context.Context, tx *sql.Tx, want int, query string, args ...any) error {
	var n int
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return err
	}
	if n != want {
		return fmt.Errorf("notification storage read back %d records, expected %d", n, want)
	}
	return nil
}
