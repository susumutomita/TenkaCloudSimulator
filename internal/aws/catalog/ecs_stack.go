package catalog

import (
	"database/sql"
	"fmt"
	"sync"
	"time"
)

// InitECSTaskStorage makes the native catalog schema available to a standalone stack store.
func InitECSTaskStorage(db *sql.DB) error { _, err := newStore(db); return err }

// NextECSTaskRevision works with a read-only planner or the stack write transaction.
func NextECSTaskRevision(db interface{ QueryRow(string, ...any) *sql.Row }, region, account, family string) (int, error) {
	prefix := fmt.Sprintf("arn:aws:ecs:%s:%s:task-definition/%s:", region, account, family)
	var revision int
	err := db.QueryRow(`SELECT COALESCE(MAX(json_extract(document,'$.revision')),0)+1
FROM aws_catalog_resources WHERE service='ecs' AND region=? AND kind='taskDefinition'
AND name=? AND substr(arn,1,length(?))=?`, region, family, prefix, prefix).Scan(&revision)
	return revision, err
}

var stackECSBinding = sync.OnceValues(func() (*binding, error) { return bind(ecs) })

// StackTaskDefinition is one validated native RegisterTaskDefinition request.
type StackTaskDefinition struct {
	resource *Resource
	state    row
	account  string
}

// PrepareStackTaskDefinition uses the same SDK input shape, Fargate validation,
// tags, metadata and server-controlled fields as a native registration.
func PrepareStackTaskDefinition(region, account string, revision int, input map[string]any) (*StackTaskDefinition, error) {
	b, err := stackECSBinding()
	if err != nil {
		return nil, err
	}
	op := b.ops["RegisterTaskDefinition"]
	official := b.model.Operations[op.Name]
	d := decoder{model: b.model}
	value, err := d.decode(official.Input, input, official.Input, 0)
	if err != nil {
		return nil, err
	}
	members := value.(map[string]any)
	if err := ecsTaskDefinition(members); err != nil {
		return nil, err
	}
	q := request{b: b, e: &Engine{cfg: Config{Account: account, Now: time.Now}}, op: op, official: official, region: region, input: members}
	r, _, err := q.prepareCreate(revision)
	if err != nil {
		return nil, err
	}
	return &StackTaskDefinition{resource: q.resource(), state: r, account: account}, nil
}

func (d *StackTaskDefinition) ARN() string { return d.state.ARN }
func (d *StackTaskDefinition) Authorize(authorize func(string, string) error) error {
	if err := authorize("ecs:RegisterTaskDefinition", d.ARN()); err != nil {
		return err
	}
	return ecsRegistrationPermissions(d.account, d.state, authorize)
}

// Register checks the planned revision while holding the stack write transaction.
func (d *StackTaskDefinition) Register(tx *sql.Tx) error {
	next, err := NextECSTaskRevision(tx, d.state.Region, d.account, d.state.Name)
	if err != nil {
		return err
	}
	if next != d.state.Document["revision"] {
		return fmt.Errorf("task definition family advanced before stack creation; retry CreateStack")
	}
	return insertNewResource(tx, d.resource, &d.state, nil)
}

// DeregisterStackTaskDefinition retires only the ARN owned by this stack.
func DeregisterStackTaskDefinition(tx *sql.Tx, region, arn string) error {
	r, err := findARN(tx, "ecs", arn)
	if err != nil {
		return err
	}
	if r.Region != region || r.Kind != "taskDefinition" {
		return fmt.Errorf("task definition is outside stack scope")
	}
	if r.Document["status"] == "INACTIVE" {
		return nil
	}
	r.Document["status"] = "INACTIVE"
	r.Document["deregisteredAt"] = time.Now().UTC().Format(time.RFC3339Nano)
	return updateRow(tx, r)
}
