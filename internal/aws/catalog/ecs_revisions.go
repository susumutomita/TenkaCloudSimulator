package catalog

import (
	"database/sql"
	"errors"
	"regexp"
	"sort"
	"strings"
)

func (q *request) isECSTaskDefinition() bool {
	return q.b.spec.Model == "ecs" && q.op.Resource == "taskDefinition"
}

// Choose a candidate before evaluating IAM, which uses the same SQLite
// connection. The unique ARN is claimed by the create transaction. If another
// registration won that revision, its successor must be authorized separately.
func (q *request) registerECSRevision() (map[string]any, error) {
	family := q.input["family"].(string) // ecsTaskDefinition already validated this member.
	previous := 0
	for {
		if err := q.ctx.Err(); err != nil {
			return nil, err
		}
		revision, err := NextECSTaskRevision(q.e.store.db, q.region, q.e.cfg.Account, family)
		if err != nil {
			return nil, err
		}
		if revision <= previous {
			return nil, &fault{kind: "conflict", message: "The stored task definition revision conflicts with its ARN."}
		}
		previous = revision
		out, err := q.create(revision)
		var f *fault
		if !errors.As(err, &f) || f.kind != "conflict" {
			return out, err
		}
	}
}

var ecsRoleName = regexp.MustCompile(`^[A-Za-z0-9+=,.@_-]{1,64}$`)

func ecsRegistrationPermissions(account string, r row, authorize func(string, string) error) error {
	prefix := "arn:aws:iam::" + account + ":role/"
	for _, member := range []string{"executionRoleArn", "taskRoleArn"} {
		if role, ok := r.Document[member].(string); ok && role != "" {
			if ecsRoleName.MatchString(role) {
				role = prefix + role
			}
			path, local := strings.CutPrefix(role, prefix)
			if !local || path == "" || strings.ContainsAny(path, "*?") || strings.HasSuffix(path, "/") {
				return Failure("ClientException", "The task role must name a role in the simulator account.")
			}
			if err := authorize("iam:PassRole", role); err != nil {
				return err
			}
		}
	}
	if len(r.Tags) != 0 {
		return authorize("ecs:TagResource", r.ARN)
	}
	return nil
}

func (q *request) lookupECSRevision(identifier string) (row, error) {
	prefix := q.formatARN(q.resource(), "", "", nil)
	key := strings.TrimPrefix(identifier, prefix)
	arn := prefix + key
	var found row
	var err error
	if ecsName.MatchString(key) && key == identifier && q.op.Kind == Read {
		// The legacy row has id=family. Resolve by family and registration
		// order so that it remains revision 1 after upgrading the database.
		found, err = scanRow(q.e.store.db.QueryRow("SELECT "+columns+` FROM aws_catalog_resources
WHERE service='ecs' AND region=? AND kind='taskDefinition' AND name=?
AND substr(arn,1,length(?))=? AND json_extract(document,'$.status')='ACTIVE'
ORDER BY seq DESC LIMIT 1`, q.region, key, prefix, prefix))
		arn += ":*"
	} else {
		found, err = findARN(q.e.store.db, "ecs", arn)
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return row{}, err
	}
	if err == nil {
		arn = found.ARN
	}
	if err := q.authorize(arn); err != nil {
		return row{}, err
	}
	if errors.Is(err, sql.ErrNoRows) || found.Document["status"] != "ACTIVE" {
		return row{}, Failure("ClientException", "The active task definition revision does not exist.")
	}
	return found, nil
}

// A cursor identifies a retained revision, so list can continue in AWS family
// and revision order even if the cursor is deregistered between requests.
func (q *request) listECSRevisions(rows []row, parent *row, inputToken, outputToken, limitKey string, limit int) (map[string]any, error) {
	family, _ := q.input["familyPrefix"].(string)
	status, _ := q.input["status"].(string)
	if status == "INACTIVE" {
		return nil, Failure("InvalidParameterException", "Listing INACTIVE task definitions is not supported: the simulator does not track live ECS task or service references.")
	}
	order, _ := q.input["sort"].(string)
	cursorScope := fingerprint(q.input, inputToken, limitKey)
	cursor := int64(0)
	if token, ok := q.input[inputToken].(string); ok && token != "" {
		var valid bool
		cursor, valid = pageAfter(token, q.op.Name, q.region, cursorScope)
		if !valid {
			return nil, &fault{kind: "token", message: "Invalid pagination token for this request."}
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if order == "DESC" {
			a, b = b, a
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Seq < b.Seq
	})
	prefix := q.formatARN(q.resource(), "", "", nil)
	page := []row{}
	next := ""
	for _, r := range rows {
		if cursor != 0 {
			if r.Seq == cursor {
				cursor = 0
			}
			continue
		}
		if !strings.HasPrefix(r.ARN, prefix) || (family != "" && r.Name != family) || (status != "" && status != "ACTIVE") || r.Document["status"] != "ACTIVE" {
			continue
		}
		if len(page) == limit {
			next = pageToken(q.op.Name, q.region, cursorScope, page[len(page)-1].Seq)
			break
		}
		page = append(page, r)
	}
	return q.listOutput(page, parent, outputToken, next), nil
}
