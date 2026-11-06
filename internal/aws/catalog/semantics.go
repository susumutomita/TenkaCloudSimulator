package catalog

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
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
)

type request struct {
	e         *Engine
	b         *binding
	op        *Op
	official  *Operation
	region    string
	principal string
	input     map[string]any
	ctx       context.Context
	w         http.ResponseWriter
}

func (q *request) execute() (map[string]any, error) {
	for _, member := range q.b.spec.AccountMembers {
		if value, ok := q.input[member].(string); ok && value != q.e.cfg.Account {
			return nil, invalid("%s must be the simulator account %s.", member, q.e.cfg.Account)
		}
	}
	if q.op.Check != nil {
		if err := q.op.Check(q.input); err != nil {
			return nil, err
		}
	}
	switch q.op.Kind {
	case Create:
		return q.create()
	case Read:
		return q.read()
	case List:
		return q.list()
	case Update:
		return q.update()
	case Delete:
		return q.remove()
	case Put:
		return q.put()
	case Tag, Untag:
		return q.tag()
	}
	return q.listTags()
}

// identifier is the value that names the target of this operation.
func (q *request) identifier() string {
	if q.resource() != nil && q.resource().Singleton {
		return "default"
	}
	value, _ := q.input[q.op.Lookup].(string)
	return value
}

// put creates the identified resource or replaces the supplied members of
// the existing one; generated identity members are preserved.
func (q *request) put() (map[string]any, error) {
	res := q.resource()
	parent, err := q.parentRow(res)
	if err != nil {
		return nil, err
	}
	found, err := find(q.e.store.db, q.b.spec.Model, q.region, res.Kind, parentID(parent), q.identifier())
	if errors.Is(err, sql.ErrNoRows) {
		return q.create()
	}
	if err != nil {
		return nil, err
	}
	if err := q.authorize(found.ARN); err != nil {
		return nil, err
	}
	resourceShape := q.b.model.shape(res.Shape)
	tagsMember := q.tagMember(q.official.Input)
	for key, value := range q.members() {
		if key == tagsMember || q.passive(key) && key != res.NameMember {
			continue
		}
		target := key
		if alias, ok := res.Aliases[key]; ok {
			target = alias
		}
		if _, ok := resourceShape.Members[target]; !ok {
			return nil, &fault{kind: "unsupported", message: fmt.Sprintf("Member %s of %s is not supported by the simulator.", key, q.op.Name)}
		}
		found.Document[target] = stored(value)
	}
	if value, ok := q.input[tagsMember]; ok && tagsMember != "" {
		for k, v := range q.tagsFrom(q.b.model.shape(q.official.Input).Members[tagsMember].Shape, value) {
			found.Tags[k] = v
		}
		if len(found.Tags) > maxTagsPerResource {
			return nil, &fault{kind: "limit", message: fmt.Sprintf("At most %d tags are supported per resource.", maxTagsPerResource)}
		}
	}
	for _, key := range q.op.Touch {
		found.Document[key] = q.now().Format(time.RFC3339Nano)
	}
	for key, value := range q.op.Set {
		found.Document[key] = q.generated(value, found, parent)
	}
	if err := q.checkRefs(res, found.Document); err != nil {
		return nil, err
	}
	if err := q.commit(func(tx *sql.Tx) error { return updateRow(tx, found) }); err != nil {
		return nil, err
	}
	return q.output(found, parent), nil
}

// members flattens the From structure into the top-level input members.
func (q *request) members() map[string]any {
	members := map[string]any{}
	for key, value := range q.input {
		if key != q.op.From || q.op.From == "" {
			members[key] = value
		}
	}
	if nested, ok := q.input[q.op.From].(map[string]any); ok {
		for key, value := range nested {
			members[key] = value
		}
	}
	return members
}

// passive reports input members that carry no state change of their own.
func (q *request) passive(key string) bool {
	if key == q.op.Lookup || key == q.op.Parent || key == q.op.Names {
		return true
	}
	for _, member := range q.op.Accept {
		if key == member {
			return true
		}
	}
	for _, member := range q.b.spec.AccountMembers {
		if key == member {
			return true
		}
	}
	return false
}

// rejectUnconsumed refuses input members the bounded operation would ignore.
func (q *request) rejectUnconsumed(consumed map[string]bool) error {
	keys := make([]string, 0, len(q.input))
	for key := range q.input {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !consumed[key] && !q.passive(key) {
			return &fault{kind: "unsupported", message: fmt.Sprintf("Member %s of %s is not supported by the simulator.", key, q.op.Name)}
		}
	}
	return nil
}

// checkRefs requires every referenced ARN to name an existing resource.
func (q *request) checkRefs(res *Resource, document map[string]any) error {
	for member, kind := range res.Refs {
		arn, ok := document[member].(string)
		if !ok {
			continue
		}
		found, err := findARN(q.e.store.db, q.b.spec.Model, arn)
		if errors.Is(err, sql.ErrNoRows) || err == nil && found.Kind != kind {
			return &fault{kind: "notfound", message: fmt.Sprintf("The referenced %s %s does not exist.", kind, arn)}
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (q *request) authorize(resource string) error {
	decision, err := q.e.cfg.Authorize(q.ctx, q.principal, q.b.spec.IAMPrefix+":"+q.op.Name, resource)
	if err != nil {
		return err
	}
	q.w.Header().Set("X-Simulator-IAM-Decision", decision)
	if decision != "allowed" {
		return &fault{kind: "denied", message: "IAM evaluation: " + decision}
	}
	return nil
}

func (q *request) resource() *Resource { return q.b.spec.resource(q.op.Resource) }

func (q *request) arnRegion() string {
	if q.b.spec.Global {
		return ""
	}
	return q.region
}

func (q *request) formatARN(res *Resource, id, name string, parent *row) string {
	replacer := []string{"{partition}", "aws", "{region}", q.arnRegion(), "{account}", q.e.cfg.Account, "{id}", id, "{name}", name}
	if parent != nil {
		replacer = append(replacer, "{parent}", parent.ID, "{parentName}", parent.Name)
	} else {
		replacer = append(replacer, "{parent}", "*", "{parentName}", "*")
	}
	return strings.NewReplacer(replacer...).Replace(res.ARN)
}

// parentRow resolves the parent identified by the operation's parent member.
func (q *request) parentRow(res *Resource) (*row, error) {
	if res.Parent == "" {
		return nil, nil
	}
	identifier, _ := q.input[q.op.Parent].(string)
	parentRes := q.b.spec.resource(res.Parent)
	found, err := find(q.e.store.db, q.b.spec.Model, q.region, parentRes.Kind, "", identifier)
	if errors.Is(err, sql.ErrNoRows) {
		if err := q.authorize(q.formatARN(parentRes, identifier, identifier, nil)); err != nil {
			return nil, err
		}
		return nil, &fault{kind: "notfound", message: fmt.Sprintf("The %s %s does not exist.", parentRes.Kind, identifier)}
	}
	if err != nil {
		return nil, err
	}
	return &found, nil
}

func parentID(parent *row) string {
	if parent == nil {
		return ""
	}
	return parent.ID
}

// lookup resolves the identified resource, authorizing before disclosure.
func (q *request) lookup() (row, *row, error) {
	res := q.resource()
	parent, err := q.parentRow(res)
	if err != nil {
		return row{}, nil, err
	}
	identifier := q.identifier()
	found, err := find(q.e.store.db, q.b.spec.Model, q.region, res.Kind, parentID(parent), identifier)
	if errors.Is(err, sql.ErrNoRows) && res.Singleton {
		r := row{Service: q.b.spec.Model, Region: q.region, Kind: res.Kind, ID: "default", Name: "default", Tags: map[string]string{}, Document: map[string]any{}}
		r.ARN = q.formatARN(res, r.ID, r.Name, parent)
		if err := q.authorize(r.ARN); err != nil {
			return row{}, nil, err
		}
		for key, value := range res.Generate {
			if generated := q.generated(value, r, parent); generated != nil {
				r.Document[key] = generated
			}
		}
		return r, parent, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		guess := identifier
		if !strings.HasPrefix(identifier, "arn:") {
			guess = q.formatARN(res, identifier, identifier, parent)
		}
		if err := q.authorize(guess); err != nil {
			return row{}, nil, err
		}
		return row{}, nil, &fault{kind: "notfound", message: fmt.Sprintf("The %s %s does not exist.", res.Kind, identifier)}
	}
	if err != nil {
		return row{}, nil, err
	}
	if err := q.authorize(found.ARN); err != nil {
		return row{}, nil, err
	}
	return found, parent, nil
}

func newID(generator, name string) (string, error) {
	kind, rest, _ := strings.Cut(generator, ":")
	switch kind {
	case "uuid":
		var b [16]byte
		if _, err := rand.Read(b[:]); err != nil {
			return "", err
		}
		b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
		h := hex.EncodeToString(b[:])
		return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:], nil
	case "name":
		return name, nil
	case "alnum", "hex":
		sizeText, prefix, _ := strings.Cut(rest, ":")
		size, err := strconv.Atoi(sizeText)
		if err != nil || size < 4 || size > 64 {
			return "", errors.New("identifier generator needs a 4-64 character size")
		}
		alphabet := "0123456789abcdefghijklmnopqrstuvwxyz"
		if kind == "hex" {
			alphabet = "0123456789abcdef"
		}
		var b = make([]byte, size)
		if _, err := rand.Read(b); err != nil {
			return "", err
		}
		for i := range b {
			b[i] = alphabet[int(b[i])%len(alphabet)]
		}
		return prefix + string(b), nil
	}
	return "", errors.New("unknown identifier generator " + generator)
}

// tagLike reports how a shape represents tags: "list" of key/value
// structures, "map" of strings, or "" when the shape is not a tag shape.
func (m *Model) tagLike(shapeName string) (string, string, string) {
	shape := m.shape(shapeName)
	if shape.Type == "map" && m.shape(shape.Key.Shape).Type == "string" && m.shape(shape.Value.Shape).Type == "string" {
		return "map", "", ""
	}
	if shape.Type != "list" {
		return "", "", ""
	}
	element := m.shape(shape.Member.Shape)
	if element.Type != "structure" || len(element.Members) != 2 {
		return "", "", ""
	}
	for _, pair := range [][2]string{{"Key", "Value"}, {"key", "value"}, {"TagKey", "TagValue"}, {"tagKey", "tagValue"}} {
		if _, k := element.Members[pair[0]]; k {
			if _, v := element.Members[pair[1]]; v {
				return "list", pair[0], pair[1]
			}
		}
	}
	return "", "", ""
}

func (q *request) tagMember(shapeName string) string {
	shape := q.b.model.shape(shapeName)
	for _, name := range shape.sortedMembers() {
		if kind, _, _ := q.b.model.tagLike(shape.Members[name].Shape); kind != "" && strings.Contains(strings.ToLower(name), "tag") {
			return name
		}
	}
	return ""
}

func (q *request) tagsFrom(shapeName string, value any) map[string]string {
	kind, keyName, valueName := q.b.model.tagLike(shapeName)
	tags := map[string]string{}
	switch kind {
	case "map":
		for k, v := range value.(map[string]any) {
			tags[k] = v.(string)
		}
	case "list":
		for _, item := range value.([]any) {
			pair := item.(map[string]any)
			key, _ := pair[keyName].(string)
			val, _ := pair[valueName].(string)
			tags[key] = val
		}
	}
	return tags
}

func (q *request) renderTags(shapeName string, tags map[string]string) any {
	kind, keyName, valueName := q.b.model.tagLike(shapeName)
	if kind == "map" {
		out := map[string]any{}
		for k, v := range tags {
			out[k] = v
		}
		return out
	}
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]any, 0, len(keys))
	for _, k := range keys {
		out = append(out, map[string]any{keyName: k, valueName: tags[k]})
	}
	return out
}

func idempotencyMember(shape *Shape) string {
	for _, name := range shape.sortedMembers() {
		if shape.Members[name].IdempotencyTok {
			return name
		}
	}
	return ""
}

func fingerprint(input map[string]any, skip string) string {
	clone := map[string]any{}
	for k, v := range input {
		if k != skip {
			clone[k] = stored(v)
		}
	}
	data, _ := json.Marshal(clone)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func (q *request) now() time.Time { return q.e.cfg.Now().UTC().Truncate(time.Millisecond) }

func (q *request) generated(value any, r row, parent *row) any {
	text, ok := value.(string)
	if !ok || !strings.HasPrefix(text, "$") {
		return value
	}
	if strings.HasPrefix(text, "$fmt:") {
		return strings.NewReplacer("{partition}", "aws", "{region}", q.arnRegion(), "{account}", q.e.cfg.Account, "{id}", r.ID, "{name}", r.Name).Replace(strings.TrimPrefix(text, "$fmt:"))
	}
	switch text {
	case "$arn":
		return r.ARN
	case "$id":
		return r.ID
	case "$name":
		return r.Name
	case "$now":
		return q.now().Format(time.RFC3339Nano)
	case "$uuid":
		id, _ := newID("uuid", "")
		return id
	case "$account":
		return q.e.cfg.Account
	case "$region":
		return q.arnRegion()
	case "$parentArn":
		if parent != nil {
			return parent.ARN
		}
	case "$parentId":
		if parent != nil {
			return parent.ID
		}
	case "$parentName":
		if parent != nil {
			return parent.Name
		}
	}
	return nil
}

func (q *request) create() (map[string]any, error) {
	res := q.resource()
	inputShape := q.b.model.shape(q.official.Input)
	resourceShape := q.b.model.shape(res.Shape)
	parent, err := q.parentRow(res)
	if err != nil {
		return nil, err
	}
	tagsMember := q.tagMember(q.official.Input)
	tokenMember := idempotencyMember(inputShape)
	name := ""
	document := map[string]any{}
	members := q.members()
	for key, value := range members {
		// A Put names its resource with the lookup member, which is stored.
		if key == tagsMember || key == tokenMember || q.passive(key) && !(q.op.Kind == Put && key == q.op.Lookup) {
			continue
		}
		target := key
		if alias, ok := res.Aliases[key]; ok {
			target = alias
		}
		if _, ok := resourceShape.Members[target]; !ok {
			return nil, &fault{kind: "unsupported", message: fmt.Sprintf("Member %s of %s is not supported by the simulator.", key, q.op.Name)}
		}
		document[target] = stored(value)
	}
	if res.NameMember != "" {
		name, _ = document[res.NameMember].(string)
	}
	id, err := newID(res.ID, name)
	if err != nil {
		return nil, err
	}
	if res.Singleton {
		id, name = "default", "default"
	}
	if name == "" {
		name = id
	}
	if id == "" {
		return nil, invalid("A name is required to identify this resource.")
	}
	tags := map[string]string{}
	if tagsMember != "" {
		if value, ok := q.input[tagsMember]; ok {
			tags = q.tagsFrom(inputShape.Members[tagsMember].Shape, value)
		}
	}
	if len(tags) > maxTagsPerResource {
		return nil, invalid("At most %d tags are supported per resource.", maxTagsPerResource)
	}
	r := row{Service: q.b.spec.Model, Region: q.region, Kind: res.Kind, Parent: parentID(parent), ID: id, Name: name, Tags: tags}
	r.ARN = q.formatARN(res, id, name, parent)
	if res.ARNMember != "" {
		document[res.ARNMember] = r.ARN
	}
	if res.IDMember != "" {
		document[res.IDMember] = r.ID
	}
	keys := make([]string, 0, len(res.Generate))
	for key := range res.Generate {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if _, supplied := document[key]; supplied && !strings.HasPrefix(fmt.Sprint(res.Generate[key]), "$") {
			continue // A literal default never overrides a supplied official member.
		}
		if value := q.generated(res.Generate[key], r, parent); value != nil {
			document[key] = value
		}
	}
	for _, key := range q.op.Touch {
		document[key] = q.now().Format(time.RFC3339Nano)
	}
	for key, value := range q.op.Set {
		document[key] = q.generated(value, r, parent)
	}
	r.Document = document
	if err := q.authorize(r.ARN); err != nil {
		return nil, err
	}
	if err := q.checkRefs(res, document); err != nil {
		return nil, err
	}
	token, _ := q.input[tokenMember].(string)
	request := fingerprint(q.input, tokenMember)
	tx, err := q.e.store.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if token != "" {
		var previous, arn string
		err := tx.QueryRow("SELECT fingerprint, arn FROM aws_catalog_idempotency WHERE service=? AND region=? AND operation=? AND token=? AND created>?", r.Service, r.Region, q.op.Name, token, q.now().Unix()-idempotencyLifetime).Scan(&previous, &arn)
		if err == nil {
			if previous != request {
				return nil, &fault{kind: "mismatch", message: "The client token was already used with different parameters."}
			}
			existing, err := findARN(tx, r.Service, arn)
			if err != nil {
				return nil, &fault{kind: "conflict", message: "The resource created with this client token no longer exists."}
			}
			return q.output(existing, parent), nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	limit := res.Limit
	if limit == 0 {
		limit = defaultKindLimit
	}
	count, err := countKind(tx, r.Service, r.Region, r.Kind, r.Parent)
	if err != nil {
		return nil, err
	}
	if count >= limit {
		return nil, &fault{kind: "limit", message: fmt.Sprintf("The simulator allows at most %d %s resources per scope.", limit, res.Kind)}
	}
	if res.Unique {
		taken, err := nameTaken(tx, r.Service, r.Region, r.Kind, r.Parent, r.Name)
		if err != nil {
			return nil, err
		}
		if taken {
			return nil, &fault{kind: "conflict", message: fmt.Sprintf("The %s %s already exists.", res.Kind, r.Name)}
		}
	}
	total, err := storedBytes(tx)
	if err != nil {
		return nil, err
	}
	if total > maxStoredBytes {
		return nil, &fault{kind: "limit", message: "The simulator catalog metadata budget is exhausted."}
	}
	if r.Seq, err = nextSeq(tx); err != nil {
		return nil, err
	}
	if err := insertRow(tx, r); err != nil {
		return nil, err
	}
	if token != "" {
		if _, err := tx.Exec("DELETE FROM aws_catalog_idempotency WHERE created<=?", q.now().Unix()-idempotencyLifetime); err != nil {
			return nil, err
		}
		var rows int
		if err := tx.QueryRow("SELECT count(*) FROM aws_catalog_idempotency").Scan(&rows); err != nil {
			return nil, err
		}
		if rows >= maxIdempotencyRows {
			return nil, &fault{kind: "limit", message: "The simulator client-token budget is exhausted."}
		}
		if err := exactlyOne(tx.Exec("INSERT INTO aws_catalog_idempotency VALUES(?,?,?,?,?,?,?)", r.Service, r.Region, q.op.Name, token, request, r.ARN, q.now().Unix())); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return q.output(r, parent), nil
}

func (q *request) read() (map[string]any, error) {
	if err := q.rejectUnconsumed(nil); err != nil {
		return nil, err
	}
	r, parent, err := q.lookup()
	if err != nil {
		return nil, err
	}
	return q.output(r, parent), nil
}

func (q *request) update() (map[string]any, error) {
	r, parent, err := q.lookup()
	if err != nil {
		return nil, err
	}
	res := q.resource()
	resourceShape := q.b.model.shape(res.Shape)
	allowed := map[string]bool{}
	for _, f := range q.op.Fields {
		allowed[f] = true
	}
	members := q.members()
	for key, value := range members {
		if q.passive(key) {
			continue
		}
		target := key
		if alias, ok := res.Aliases[key]; ok {
			target = alias
		}
		if _, ok := resourceShape.Members[target]; !ok || !allowed[key] {
			return nil, &fault{kind: "unsupported", message: fmt.Sprintf("Member %s of %s is not supported by the simulator.", key, q.op.Name)}
		}
		r.Document[target] = stored(value)
	}
	for _, key := range q.op.Touch {
		r.Document[key] = q.now().Format(time.RFC3339Nano)
	}
	for key, value := range q.op.Set {
		r.Document[key] = q.generated(value, r, parent)
	}
	if err := q.checkRefs(res, r.Document); err != nil {
		return nil, err
	}
	if err := q.commit(func(tx *sql.Tx) error {
		if r.Seq == 0 {
			// A singleton that was never stored is created by its first update.
			seq, err := nextSeq(tx)
			if err != nil {
				return err
			}
			r.Seq = seq
			return insertRow(tx, r)
		}
		return updateRow(tx, r)
	}); err != nil {
		return nil, err
	}
	return q.output(r, parent), nil
}

func (q *request) commit(change func(*sql.Tx) error) error {
	tx, err := q.e.store.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := change(tx); err != nil {
		var f *fault
		if errors.As(err, &f) {
			return err
		}
		if strings.Contains(err.Error(), "exactly one") {
			return &fault{kind: "conflict", message: "The resource changed concurrently; read it again before retrying."}
		}
		return err
	}
	return tx.Commit()
}

func (q *request) children(res *Resource) []string {
	var kinds []string
	for _, child := range q.b.spec.Resources {
		if child.Parent == res.Kind {
			kinds = append(kinds, child.Kind)
		}
	}
	return kinds
}

func (q *request) remove() (map[string]any, error) {
	if err := q.rejectUnconsumed(nil); err != nil {
		return nil, err
	}
	r, parent, err := q.lookup()
	if err != nil {
		return nil, err
	}
	res := q.resource()
	kinds := q.children(res)
	err = q.commit(func(tx *sql.Tx) error {
		if len(kinds) > 0 {
			n, err := countChildren(tx, r.Service, r.Region, r.ID, kinds)
			if err != nil {
				return err
			}
			if n > 0 && !res.Cascade {
				return &fault{kind: "dependents", message: fmt.Sprintf("The %s %s still has dependent resources.", res.Kind, r.Name)}
			}
			if err := deleteChildren(tx, r.Service, r.Region, r.ID, kinds); err != nil {
				return err
			}
		}
		return deleteRow(tx, r)
	})
	if err != nil {
		return nil, err
	}
	for key, value := range q.op.Set {
		r.Document[key] = q.generated(value, r, parent)
	}
	return q.output(r, parent), nil
}

// pagination returns the official paginator members, or the conventional
// NextToken/MaxResults members when the model declares no paginator.
func (q *request) pagination() (string, string, string) {
	if p := q.official.Paginator; p != nil {
		inputToken, _ := p.InputToken.(string)
		outputToken, _ := p.OutputToken.(string)
		return inputToken, outputToken, p.LimitKey
	}
	var inputToken, outputToken, limitKey string
	in := q.b.model.shape(q.official.Input)
	out := q.b.model.Shapes[q.official.Output]
	for _, name := range []string{"NextToken", "nextToken"} {
		if _, ok := in.Members[name]; ok && out != nil {
			if _, ok := out.Members[name]; ok {
				inputToken, outputToken = name, name
			}
		}
	}
	for _, name := range []string{"MaxResults", "maxResults"} {
		if _, ok := in.Members[name]; ok {
			limitKey = name
		}
	}
	return inputToken, outputToken, limitKey
}

// maxListLimit bounds any page size before it is converted to int.
const maxListLimit = 1000

type pageToken struct {
	Op     string `json:"o"`
	Region string `json:"r"`
	Scope  string `json:"s"`
	After  int64  `json:"a"`
}

func (q *request) list() (map[string]any, error) {
	res := q.resource()
	parent, err := q.parentRow(res)
	if err != nil {
		return nil, err
	}
	inputToken, outputToken, limitKey := q.pagination()
	consumed := map[string]bool{q.op.Parent: true, inputToken: true, limitKey: true}
	for member := range q.op.Filters {
		consumed[member] = true
	}
	if err := q.rejectUnconsumed(consumed); err != nil {
		return nil, err
	}
	limit := 100
	if limitKey != "" {
		member := q.b.model.shape(q.official.Input).Members[limitKey]
		if max := q.b.model.shape(member.Shape).Max; max != nil && int(*max) < limit {
			limit = int(*max)
		}
		if n, ok := q.input[limitKey].(int64); ok {
			if n < 1 {
				return nil, invalid("%s must be at least 1.", limitKey)
			}
			// The bound check keeps the conversion within int on every platform.
			if n <= maxListLimit && int(n) < limit {
				limit = int(n)
			}
		}
	}
	if err := q.authorize(q.formatARN(res, "*", "*", parent)); err != nil {
		return nil, err
	}
	rows, err := list(q.e.store.db, q.b.spec.Model, q.region, res.Kind, parentID(parent))
	if err != nil {
		return nil, err
	}
	filtered := rows[:0]
	for _, r := range rows {
		keep := true
		for member, stored := range q.op.Filters {
			if want, ok := q.input[member]; ok && fmt.Sprint(r.Document[stored]) != fmt.Sprint(want) {
				keep = false
			}
		}
		if keep {
			filtered = append(filtered, r)
		}
	}
	var failures []any
	if names, ok := q.input[q.op.Names].([]any); ok {
		selected := []row{}
		for _, name := range names {
			identifier, _ := name.(string)
			var match *row
			for i := range filtered {
				if filtered[i].Name == identifier || filtered[i].ARN == identifier || filtered[i].ID == identifier {
					match = &filtered[i]
				}
			}
			if match == nil && q.op.Failures != "" {
				item := map[string]any{}
				for key, value := range q.op.FailureItem {
					if text, ok := value.(string); ok {
						value = strings.ReplaceAll(text, "$name", identifier)
					}
					item[key] = value
				}
				failures = append(failures, item)
				continue
			}
			if match == nil {
				return nil, &fault{kind: "notfound", message: fmt.Sprintf("The %s %s does not exist.", res.Kind, identifier)}
			}
			selected = append(selected, *match)
		}
		filtered = selected
	}
	// A token is bound to the same filters and parent, not to the page size.
	clone := map[string]any{}
	for k, v := range q.input {
		if k != inputToken && k != limitKey {
			clone[k] = v
		}
	}
	scope := fingerprint(clone, "")
	start := int64(0)
	if text, ok := q.input[inputToken].(string); ok && text != "" {
		raw, err := base64.RawURLEncoding.DecodeString(text)
		var token pageToken
		if err != nil || json.Unmarshal(raw, &token) != nil || token.Op != q.op.Name || token.Region != q.region || token.Scope != scope {
			return nil, &fault{kind: "token", message: "Invalid pagination token for this request."}
		}
		start = token.After
	}
	page := []row{}
	for _, r := range filtered {
		if r.Seq > start {
			page = append(page, r)
		}
	}
	next := ""
	if outputToken != "" && len(page) > limit {
		page = page[:limit]
		data, _ := json.Marshal(pageToken{q.op.Name, q.region, scope, page[len(page)-1].Seq})
		next = base64.RawURLEncoding.EncodeToString(data)
	}
	out := q.listOutput(page, parent, outputToken, next)
	if q.op.Failures != "" {
		enc := &encoder{model: q.b.model, protocol: q.b.model.Metadata.Protocol}
		if failures == nil {
			failures = []any{}
		}
		if value, ok := enc.project(q.b.model.shape(q.official.Output).Members[q.op.Failures].Shape, failures, 0); ok {
			out[q.op.Failures] = value
		}
	}
	return out, nil
}

func (q *request) listOutput(page []row, parent *row, outputToken, next string) map[string]any {
	res := q.resource()
	out := map[string]any{}
	shape := q.b.model.shape(q.official.Output)
	enc := &encoder{model: q.b.model, protocol: q.b.model.Metadata.Protocol}
	for _, name := range shape.sortedMembers() {
		member := shape.Members[name]
		target := q.b.model.shape(member.Shape)
		if target.Type != "list" {
			continue
		}
		element := target.Member.Shape
		items := []any{}
		switch {
		case element == res.Shape || element == res.Summary:
			for _, r := range page {
				if value, ok := enc.project(element, q.withTags(r, element), 0); ok {
					items = append(items, value)
				}
			}
		case q.b.model.shape(element).Type == "string" && q.op.ListValue != "":
			for _, r := range page {
				if value, ok := r.Document[q.op.ListValue].(string); ok {
					items = append(items, value)
				}
			}
		default:
			continue
		}
		out[name] = items
	}
	if next != "" {
		out[outputToken] = next
	}
	return out
}

// withTags adds the stored tag map to the document in the shape's form.
func (q *request) withTags(r row, shapeName string) map[string]any {
	doc := map[string]any{}
	for k, v := range r.Document {
		doc[k] = v
	}
	if member := q.tagMember(shapeName); member != "" {
		doc[member] = q.renderTags(q.b.model.shape(shapeName).Members[member].Shape, r.Tags)
	}
	return doc
}

// output projects a resource onto the operation's official output shape.
func (q *request) output(r row, parent *row) map[string]any {
	out := map[string]any{}
	if q.official.Output == "" {
		return out
	}
	res := q.b.spec.resource(r.Kind)
	shape := q.b.model.shape(q.official.Output)
	enc := &encoder{model: q.b.model, protocol: q.b.model.Metadata.Protocol}
	for _, name := range shape.sortedMembers() {
		member := shape.Members[name]
		if member.Location == "statusCode" {
			continue
		}
		var value any
		var ok bool
		switch {
		case member.Shape == res.Shape || member.Shape == res.Summary:
			value, ok = enc.project(member.Shape, q.withTags(r, member.Shape), 0)
		case q.b.model.isTagShape(member.Shape) && strings.Contains(strings.ToLower(name), "tag"):
			value, ok = enc.project(member.Shape, q.renderTags(member.Shape, r.Tags), 0)
		default:
			if stored, present := r.Document[name]; present {
				value, ok = enc.project(member.Shape, stored, 0)
			}
		}
		if ok {
			out[name] = value
		}
	}
	return out
}

func (m *Model) isTagShape(name string) bool {
	kind, _, _ := m.tagLike(name)
	return kind != ""
}

func (q *request) tagged() (row, error) {
	arn, _ := q.input[q.op.Lookup].(string)
	found, err := findARN(q.e.store.db, q.b.spec.Model, arn)
	if errors.Is(err, sql.ErrNoRows) {
		if err := q.authorize(arn); err != nil {
			return row{}, err
		}
		return row{}, &fault{kind: "notfound", message: "The resource " + arn + " does not exist."}
	}
	if err != nil {
		return row{}, err
	}
	if err := q.authorize(found.ARN); err != nil {
		return row{}, err
	}
	return found, nil
}

func (q *request) tag() (map[string]any, error) {
	r, err := q.tagged()
	if err != nil {
		return nil, err
	}
	shape := q.b.model.shape(q.official.Input)
	for key := range q.input {
		if key == q.op.Lookup {
			continue
		}
		member := shape.Members[key]
		target := q.b.model.shape(member.Shape)
		switch {
		case q.op.Kind == Tag && q.b.model.isTagShape(member.Shape):
			for k, v := range q.tagsFrom(member.Shape, q.input[key]) {
				r.Tags[k] = v
			}
		case q.op.Kind == Untag && target.Type == "list" && q.b.model.shape(target.Member.Shape).Type == "string":
			for _, k := range q.input[key].([]any) {
				delete(r.Tags, k.(string))
			}
		default:
			return nil, &fault{kind: "unsupported", message: fmt.Sprintf("Member %s of %s is not supported by the simulator.", key, q.op.Name)}
		}
	}
	if len(r.Tags) > maxTagsPerResource {
		return nil, &fault{kind: "limit", message: fmt.Sprintf("At most %d tags are supported per resource.", maxTagsPerResource)}
	}
	if err := q.commit(func(tx *sql.Tx) error { return updateRow(tx, r) }); err != nil {
		return nil, err
	}
	return map[string]any{}, nil
}

func (q *request) listTags() (map[string]any, error) {
	r, err := q.tagged()
	if err != nil {
		return nil, err
	}
	for key := range q.input {
		if key != q.op.Lookup {
			return nil, &fault{kind: "unsupported", message: fmt.Sprintf("Member %s of %s is not supported by the simulator.", key, q.op.Name)}
		}
	}
	out := map[string]any{}
	shape := q.b.model.shape(q.official.Output)
	enc := &encoder{model: q.b.model, protocol: q.b.model.Metadata.Protocol}
	for _, name := range shape.sortedMembers() {
		member := shape.Members[name]
		if q.b.model.isTagShape(member.Shape) {
			if value, ok := enc.project(member.Shape, q.renderTags(member.Shape, r.Tags), 0); ok {
				out[name] = value
			}
		}
	}
	return out, nil
}
