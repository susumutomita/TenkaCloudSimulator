package catalog

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const ecrRepositoryType = "AWS::ECR::Repository"

type repositoryTagFilter struct{ key, value string }

func (b *binding) isResourceGroupQueryOperation(name string) bool {
	return b.spec.Model == "resource-groups" && (name == "GetGroupQuery" || name == "UpdateGroupQuery" || name == "ListGroupResources")
}

// CreateGroup and ListGroups have no IAM resource type in the AWS action table.
func (b *binding) isResourceGroupServiceAction(name string) bool {
	return b.spec.Model == "resource-groups" && (name == "CreateGroup" || name == "ListGroups")
}

func parsedRepositoryTagFilter(resourceQuery any) (repositoryTagFilter, error) {
	invalidQuery := Failure("BadRequestException", "Use TAG_FILTERS_1_0 with AWS::ECR::Repository and one exact tag key and value.")
	query, _ := resourceQuery.(map[string]any)
	raw, _ := query["Query"].(string)
	if query["Type"] != "TAG_FILTERS_1_0" || len(raw) > 4096 || !uniqueGroupJSON([]byte(raw)) {
		return repositoryTagFilter{}, invalidQuery
	}
	object, err := decodeJSONObject([]byte(raw))
	if err != nil || len(object) != 2 {
		return repositoryTagFilter{}, invalidQuery
	}
	types, _ := object["ResourceTypeFilters"].([]any)
	tags, _ := object["TagFilters"].([]any)
	if len(types) != 1 || types[0] != ecrRepositoryType || len(tags) != 1 {
		return repositoryTagFilter{}, invalidQuery
	}
	tag, _ := tags[0].(map[string]any)
	key, _ := tag["Key"].(string)
	values, _ := tag["Values"].([]any)
	if len(tag) != 2 || key == "" || utf8.RuneCountInString(key) > 128 || len(values) != 1 {
		return repositoryTagFilter{}, invalidQuery
	}
	value, _ := values[0].(string)
	if value == "" || utf8.RuneCountInString(value) > 256 {
		return repositoryTagFilter{}, invalidQuery
	}
	return repositoryTagFilter{key, value}, nil
}

// encoding/json accepts repeated object keys. Both the native request and the
// serialized tag query must have one unambiguous interpretation before mutation.
func uniqueGroupJSON(data []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if !uniqueGroupValue(decoder, 0) {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

func uniqueGroupValue(d *json.Decoder, depth int) bool {
	if depth > maxDepth {
		return false
	}
	token, err := d.Token()
	if err != nil {
		return false
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return true
	}
	seen := map[string]bool{}
	for d.More() {
		if delim == '{' {
			key, err := d.Token()
			name, ok := key.(string)
			if err != nil || !ok || seen[name] {
				return false
			}
			seen[name] = true
		}
		if !uniqueGroupValue(d, depth+1) {
			return false
		}
	}
	_, err = d.Token()
	return err == nil
}

func validateGroupQuery(input map[string]any) error {
	if err := groupIdentifier(input); err != nil {
		return err
	}
	_, err := parsedRepositoryTagFilter(input["ResourceQuery"])
	return err
}

func requireStoredGroupQuery(previous, _ map[string]any) error {
	if previous["ResourceQuery"] == nil {
		return Failure("BadRequestException", "The group has no supported resource query.")
	}
	return nil
}

func (q *request) getGroupQuery() (map[string]any, error) {
	if err := q.rejectUnconsumed(nil); err != nil {
		return nil, err
	}
	r, _, err := q.lookup()
	if err != nil {
		return nil, err
	}
	if err := requireStoredGroupQuery(r.Document, nil); err != nil {
		return nil, err
	}
	return groupQueryOutput(r), nil
}

func groupQueryOutput(r row) map[string]any {
	return map[string]any{"GroupQuery": map[string]any{"GroupName": r.Name, "ResourceQuery": r.Document["ResourceQuery"]}}
}

func (q *request) listGroupResources() (map[string]any, error) {
	// Authorize the requested group and all dependent reads before disclosing
	// whether a group exists. IAM itself can use the catalog's SQLite connection.
	identifier := q.identifier()
	arn := identifier
	if !strings.HasPrefix(identifier, "arn:") {
		arn = q.formatARN(q.resource(), identifier, identifier, nil)
	}
	if q.e.cfg.AuthorizeSnapshot == nil {
		return nil, fmt.Errorf("resource group membership requires a policy snapshot")
	}
	decide, err := q.e.cfg.AuthorizeSnapshot(q.ctx, q.principal)
	if err != nil {
		return nil, err
	}
	if decide == nil {
		return nil, fmt.Errorf("resource group membership received an empty policy snapshot")
	}
	if err := q.checkDecision(decide("resource-groups:ListGroupResources", arn)); err != nil {
		return nil, err
	}
	for _, action := range []string{"cloudformation:DescribeStacks", "cloudformation:ListStackResources", "tag:GetResources"} {
		if err := q.checkDecision(decide(action, "*")); err != nil {
			return nil, err
		}
	}
	if err := q.rejectUnconsumed(map[string]bool{"MaxResults": true, "NextToken": true}); err != nil {
		return nil, err
	}
	tx, err := q.e.store.db.BeginTx(q.ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	current, err := find(tx, q.b.spec.Model, q.region, "group", "", identifier)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, &fault{kind: "notfound", message: "The resource group does not exist."}
	}
	if err != nil {
		return nil, err
	}
	if current.ARN != arn {
		return nil, fmt.Errorf("stored group ARN differs from its authorized identity")
	}
	if err := requireStoredGroupQuery(current.Document, nil); err != nil {
		return nil, err
	}
	filter, err := parsedRepositoryTagFilter(current.Document["ResourceQuery"])
	if err != nil {
		return nil, fmt.Errorf("stored group query is invalid")
	}
	if q.e.byTarget["AmazonEC2ContainerRegistry_V20150921"] == nil {
		return nil, invalid("The ECR repository catalog is unavailable.")
	}
	rows, err := list(tx, "ecr", q.region, "repository", "")
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return q.groupMembersPage(current, filter, rows)
}

func (q *request) groupMembersPage(group row, filter repositoryTagFilter, rows []row) (map[string]any, error) {
	scope := fmt.Sprintf("%s\x00%d\x00%d\x00%q\x00%q", group.ARN, group.Seq, group.Generation, filter.key, filter.value)
	var after int64
	if token, present := q.input["NextToken"]; present {
		var ok bool
		after, ok = pageAfter(token.(string), q.op.Name, q.region, scope)
		if !ok {
			return nil, invalid("Invalid membership pagination token for this group and query.")
		}
	}
	limit := int64(50)
	if count, ok := q.input["MaxResults"].(int64); ok {
		limit = count
	}
	members, identifiers := []any{}, []any{}
	output := map[string]any{}
	var last int64
	for _, r := range rows {
		if r.Seq <= after || r.Tags[filter.key] != filter.value {
			continue
		}
		if int64(len(members)) == limit {
			output["NextToken"] = pageToken(q.op.Name, q.region, scope, last)
			break
		}
		id := map[string]any{"ResourceArn": r.ARN, "ResourceType": ecrRepositoryType}
		members = append(members, map[string]any{"Identifier": id})
		identifiers = append(identifiers, id)
		last = r.Seq
	}
	output["Resources"] = members
	output["ResourceIdentifiers"] = identifiers
	return output, nil
}
