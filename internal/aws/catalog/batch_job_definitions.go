package catalog

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
)

// A job definition is inert metadata, not an image reference that any local
// code resolves or runs. This is the small image-name subset documented for
// the simulator; a URI, digest, registry port and free-form text are not accepted.
var batchDefinitionImage = regexp.MustCompile(`^[a-z0-9]+([._-][a-z0-9]+)*(/[a-z0-9]+([._-][a-z0-9]+)*)*(:[A-Za-z0-9_][A-Za-z0-9_.-]{0,127})?$`)
var batchDefinitionRevision = regexp.MustCompile(`^(arn:aws:batch:[a-z0-9-]{1,32}:[0-9]{12}:job-definition/)?[A-Za-z0-9_-]{1,128}:[1-9][0-9]{0,9}$`)

const batchDefinitionLimit = 50

func (q *request) isBatchJobDefinition() bool {
	return q.b.spec.Model == "batch" && q.op.Resource == "jobDefinition"
}

func batchDefinitionFields(fields map[string]any, allowed ...string) error {
	for key := range fields {
		if !slices.Contains(allowed, key) {
			return invalid("The bounded job definition does not support %s.", key)
		}
	}
	return nil
}

func batchRegisterDefinition(input map[string]any) error {
	if err := batchDefinitionFields(input, "jobDefinitionName", "type", "containerProperties", "platformCapabilities"); err != nil {
		return err
	}
	if err := batchName(input, "jobDefinitionName"); err != nil {
		return err
	}
	if input["type"] != "container" {
		return invalid("Only the EC2 single-container job definition type is supported.")
	}
	if caps, given := input["platformCapabilities"]; given {
		values, _ := caps.([]any)
		if len(values) != 1 || values[0] != "EC2" {
			return invalid("The only supported platform capability is EC2.")
		}
	}
	container, _ := input["containerProperties"].(map[string]any)
	if err := batchDefinitionFields(container, "image", "resourceRequirements"); err != nil {
		return err
	}
	image, _ := container["image"].(string)
	if len(image) > 255 || !batchDefinitionImage.MatchString(image) {
		return invalid("A bounded image name (optionally with a tag) is required; no image is pulled.")
	}
	resources, _ := container["resourceRequirements"].([]any)
	if len(resources) != 2 {
		return invalid("Provide exactly one MEMORY and one VCPU resource requirement.")
	}
	seen := map[string]bool{}
	for _, raw := range resources {
		resource, _ := raw.(map[string]any)
		kind, _ := resource["type"].(string)
		value, _ := resource["value"].(string)
		quantity, err := strconv.ParseInt(value, 10, 32)
		if err != nil || strconv.FormatInt(quantity, 10) != value || seen[kind] ||
			(kind != "MEMORY" && kind != "VCPU") || quantity < 1 || (kind == "MEMORY" && quantity < 4) {
			return invalid("Resource requirements must be distinct: integral VCPU of at least 1 and MEMORY of at least 4 MiB.")
		}
		seen[kind] = true
	}
	return nil
}

func batchDeregisterDefinition(input map[string]any) error {
	identifier, _ := input["jobDefinition"].(string)
	if !batchDefinitionRevision.MatchString(identifier) {
		return invalid("A job definition name:revision or revision ARN is required.")
	}
	return nil
}

func batchRequireActiveDefinition(previous, _ map[string]any) error {
	if previous["status"] != "ACTIVE" {
		return invalid("The active job definition revision does not exist.")
	}
	return nil
}

func (q *request) registerBatchDefinition() (map[string]any, error) {
	name := q.input["jobDefinitionName"].(string)
	res := q.resource()
	// RegisterJobDefinition authorizes the family, not the yet-unknown
	// revision ARN. Do IAM before entering the single-connection transaction.
	if err := q.authorize(q.formatARN(res, name, name, nil)); err != nil {
		return nil, err
	}
	tx, err := q.e.store.db.BeginTx(q.ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	revisions, err := list(tx, q.b.spec.Model, q.region, res.Kind, "")
	if err != nil {
		return nil, err
	}
	if len(revisions) >= res.Limit {
		return nil, &fault{kind: "limit", message: "The simulator job definition revision limit for this region is exhausted."}
	}
	revision := int64(1)
	for _, saved := range revisions {
		if saved.Document["jobDefinitionName"] != name {
			continue
		}
		stored, _ := saved.Document["revision"].(json.Number)
		number, err := stored.Int64()
		if err != nil || number < 1 || number >= 1<<31-1 {
			return nil, fmt.Errorf("invalid stored Batch job definition revision")
		}
		if number >= revision {
			revision = number + 1
		}
	}
	id := fmt.Sprintf("%s:%d", name, revision)
	arn := q.formatARN(res, id, name, nil)
	record := row{Service: q.b.spec.Model, Region: q.region, Kind: res.Kind, ID: id, Name: id, ARN: arn, Tags: map[string]string{},
		Document: map[string]any{
			"jobDefinitionName": name, "jobDefinitionArn": arn, "revision": revision,
			"status": "ACTIVE", "type": "container", "containerOrchestrationType": "ECS", "platformCapabilities": []any{"EC2"},
			"containerProperties": stored(q.input["containerProperties"]),
		},
	}
	if err := insertNewResource(tx, res, &record, nil); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return q.output(record, nil), nil
}

func (q *request) describeBatchDefinitions() (map[string]any, error) {
	// DescribeJobDefinitions has no resource-level IAM target. Check before
	// reading any rows, including for exact IDs and empty or paginated lists.
	if err := q.authorize("*"); err != nil {
		return nil, err
	}
	inputToken, outputToken, limitKey := q.pagination()
	picks, byRevision := q.input["jobDefinitions"].([]any)
	chosen := map[string]bool{}
	if byRevision {
		if len(q.input) != 1 || len(picks) < 1 || len(picks) > 100 {
			return nil, invalid("Provide 1 to 100 jobDefinitions identifiers without other filters or page members.")
		}
		for _, raw := range picks {
			id, _ := raw.(string)
			if !batchDefinitionRevision.MatchString(id) {
				return nil, invalid("Each jobDefinitions identifier must be name:revision or a revision ARN.")
			}
			chosen[id] = true
		}
	}
	if err := batchName(q.input, "jobDefinitionName"); err != nil {
		return nil, err
	}
	name, _ := q.input["jobDefinitionName"].(string)
	status, hasStatus := q.input["status"].(string)
	if hasStatus && status != "ACTIVE" && status != "INACTIVE" {
		return nil, invalid("The status filter must be ACTIVE or INACTIVE.")
	}
	limit := 100
	if n, given := q.input[limitKey].(int64); given {
		if n < 1 || n > 100 {
			return nil, invalid("maxResults must be between 1 and 100.")
		}
		limit = int(n)
	}
	scope := fingerprint(q.input, inputToken, limitKey)
	var after int64
	if token, given := q.input[inputToken].(string); given {
		var valid bool
		after, valid = pageAfter(token, q.op.Name, q.region, scope)
		if !valid {
			return nil, invalid("Invalid pagination token for this request.")
		}
	}
	revisions, err := list(q.e.store.db, q.b.spec.Model, q.region, q.resource().Kind, "")
	if err != nil {
		return nil, err
	}
	page := []row{}
	next := ""
	for _, saved := range revisions {
		if saved.Seq <= after || byRevision && !chosen[saved.ID] && !chosen[saved.ARN] ||
			name != "" && name != saved.Document["jobDefinitionName"] || hasStatus && status != saved.Document["status"] {
			continue
		}
		if len(page) == limit {
			next = pageToken(q.op.Name, q.region, scope, page[len(page)-1].Seq)
			break
		}
		page = append(page, saved)
	}
	return q.listOutput(page, nil, outputToken, next), nil
}
