package aws

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

// DynamoDB subset: JSON 1.0 API (DynamoDB_20120810) at the regional root
// POST endpoint, following the same wire conventions as the SSM handler.
// Supported operations: CreateTable, ListTables, DescribeTable,
// DeleteTable, PutItem, GetItem, DeleteItem. Everything else returns
// UnknownOperationException instead of silently succeeding (see
// docs/dynamodb.md for the explicit unsupported list).

type dynamoFailure struct{ code, message string }

func (e dynamoFailure) Error() string { return e.message }

func dynamoJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.0")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func dynamoError(w http.ResponseWriter, err error) {
	code, message, status := "InternalServerError", "Simulator DynamoDB state operation failed.", 500
	var e dynamoFailure
	if errors.As(err, &e) {
		code, message, status = e.code, e.message, 400
		if code == "AccessDeniedException" {
			status = 403
		}
		if code == "UnknownOperationException" {
			status = 501
		}
	}
	dynamoJSON(w, status, map[string]string{"__type": "com.amazonaws.dynamodb.v20120810#" + code, "message": message})
}

var dynamoTableName = regexp.MustCompile(`^[a-zA-Z0-9_.-]{3,255}$`)

// canonicalDynamoNumber normalizes a DynamoDB number string so that
// numerically equal keys ("1", "1.0", "1e2"/"100") identify the same item.
func canonicalDynamoNumber(s string) (string, bool) {
	canonical, _, ok := parseDynamoDecimal(s)
	return canonical, ok
}

// dynamoKeyComponent encodes one key attribute value with its type tag.
// The value must be an AttributeValue of exactly the declared key type.
func dynamoKeyComponent(value any, declared string) (string, error) {
	av, ok := value.(map[string]any)
	if !ok || len(av) != 1 {
		return "", fmt.Errorf("not a single-typed attribute value")
	}
	raw, present := av[declared]
	if !present {
		return "", fmt.Errorf("type mismatch")
	}
	s, ok := raw.(string)
	if !ok {
		return "", fmt.Errorf("key value must be a string")
	}
	switch declared {
	case "S":
		if s == "" {
			return "", fmt.Errorf("empty string key")
		}
		return "S\x00" + s, nil
	case "N":
		n, ok := canonicalDynamoNumber(s)
		if !ok || s == "" {
			return "", fmt.Errorf("invalid number key")
		}
		return "N\x00" + n, nil
	case "B":
		b, err := base64.StdEncoding.DecodeString(s)
		if err != nil || len(b) == 0 {
			return "", fmt.Errorf("invalid binary key")
		}
		return "B\x00" + string(b), nil
	}
	return "", fmt.Errorf("unsupported key type")
}

// validateDynamoValue checks one AttributeValue recursively.
func validateDynamoValue(value any, depth int) error {
	if depth > 32 {
		return dynamoFailure{"ValidationException", "Attribute values nest deeper than the supported 32 levels."}
	}
	av, ok := value.(map[string]any)
	if !ok || len(av) != 1 {
		return dynamoFailure{"ValidationException", "Each attribute value must be an object with exactly one type."}
	}
	for kind, raw := range av {
		switch kind {
		case "S":
			if _, ok := raw.(string); !ok {
				return dynamoFailure{"ValidationException", "S attribute values must be strings."}
			}
		case "N":
			s, ok := raw.(string)
			if !ok {
				return dynamoFailure{"ValidationException", "N attribute values must be strings."}
			}
			if _, ok := canonicalDynamoNumber(s); !ok {
				return dynamoFailure{"ValidationException", "Invalid number: " + s}
			}
		case "B":
			s, ok := raw.(string)
			if !ok {
				return dynamoFailure{"ValidationException", "B attribute values must be base64 strings."}
			}
			if _, err := base64.StdEncoding.DecodeString(s); err != nil {
				return dynamoFailure{"ValidationException", "Invalid base64 in B attribute value."}
			}
		case "BOOL":
			if _, ok := raw.(bool); !ok {
				return dynamoFailure{"ValidationException", "BOOL attribute values must be booleans."}
			}
		case "NULL":
			if raw != true {
				return dynamoFailure{"ValidationException", "NULL attribute values must be true."}
			}
		case "SS", "NS", "BS":
			list, ok := raw.([]any)
			if !ok || len(list) == 0 {
				return dynamoFailure{"ValidationException", kind + " attribute values must be non-empty arrays."}
			}
			seen := map[string]bool{}
			for _, el := range list {
				s, ok := el.(string)
				if !ok {
					return dynamoFailure{"ValidationException", kind + " elements must be strings."}
				}
				// Set membership compares values, not their transport spelling.
				// Preserve the supplied JSON; normalization is for equality only.
				canonical := s
				if kind == "NS" {
					canonical, ok = canonicalDynamoNumber(s)
					if !ok {
						return dynamoFailure{"ValidationException", "Invalid number in NS: " + s}
					}
				}
				if kind == "BS" {
					decoded, err := base64.StdEncoding.DecodeString(s)
					if err != nil {
						return dynamoFailure{"ValidationException", "Invalid base64 in BS element."}
					}
					canonical = string(decoded)
				}
				if seen[canonical] {
					return dynamoFailure{"ValidationException", kind + " elements must have unique values."}
				}
				seen[canonical] = true
			}
		case "L":
			list, ok := raw.([]any)
			if !ok {
				return dynamoFailure{"ValidationException", "L attribute values must be arrays."}
			}
			for _, el := range list {
				if err := validateDynamoValue(el, depth+1); err != nil {
					return err
				}
			}
		case "M":
			m, ok := raw.(map[string]any)
			if !ok {
				return dynamoFailure{"ValidationException", "M attribute values must be objects."}
			}
			for _, el := range m {
				if err := validateDynamoValue(el, depth+1); err != nil {
					return err
				}
			}
		default:
			return dynamoFailure{"ValidationException", "Unsupported attribute type: " + kind}
		}
	}
	return nil
}

// parseDynamoCondition accepts only the two bounded condition forms the
// simulator evaluates; anything else is an explicit ValidationException.
func parseDynamoCondition(expr, partitionKey string) (string, error) {
	compact := strings.Join(strings.Fields(expr), "")
	switch compact {
	case "":
		return "", nil
	case "attribute_not_exists(" + partitionKey + ")":
		return "not_exists", nil
	case "attribute_exists(" + partitionKey + ")":
		return "exists", nil
	}
	return "", dynamoFailure{"ValidationException", "Unsupported ConditionExpression; only attribute_not_exists(<partition key>) and attribute_exists(<partition key>) are simulated."}
}

type dynamoRequest struct {
	TableName            string
	AttributeDefinitions []struct {
		AttributeName string
		AttributeType string
	}
	KeySchema []struct {
		AttributeName string
		KeyType       string
	}
	BillingMode           string
	ProvisionedThroughput *struct {
		ReadCapacityUnits  int64
		WriteCapacityUnits int64
	}
	Limit                   int
	ExclusiveStartTableName string
	Item                    map[string]any
	Key                     map[string]any
	ConditionExpression     string
	ConsistentRead          bool
}

// Check duplicates before map/struct decoding can overwrite them. NULL is a
// typed AttributeValue ({"NULL":true}), never a JSON null. The structural limit
// leaves room for all 32 supported attribute levels, each with wrapper objects.
func dynamoUniqueJSON(decoder *json.Decoder, depth int) error {
	if depth > 128 {
		return errors.New("JSON structural nesting exceeds the simulator limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if token == nil {
		return errors.New("JSON null is unsupported")
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			field, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := field.(string)
			if !ok || seen[name] {
				return errors.New("duplicate JSON fields are unsupported")
			}
			seen[name] = true
			if err := dynamoUniqueJSON(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := dynamoUniqueJSON(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	_, err = decoder.Token()
	return err
}

// encoding/json accepts case-insensitive struct fields and silently drops
// unknown fields. Native request shapes must instead match their declared keys.
func dynamoExactFields(data json.RawMessage, fields []string) error {
	var object map[string]json.RawMessage
	if json.Unmarshal(data, &object) != nil || len(object) != len(fields) {
		return errors.New("nested request object must contain exactly its required fields")
	}
	for field := range object {
		if !slices.Contains(fields, field) {
			return errors.New("unsupported nested request field")
		}
	}
	return nil
}

func dynamoNestedRequestFields(raw map[string]json.RawMessage) error {
	for field, names := range map[string][]string{
		"AttributeDefinitions": {"AttributeName", "AttributeType"},
		"KeySchema":            {"AttributeName", "KeyType"},
	} {
		if data, present := raw[field]; present {
			var objects []json.RawMessage
			if json.Unmarshal(data, &objects) != nil {
				return errors.New("nested request field must be an array of objects")
			}
			for _, object := range objects {
				if err := dynamoExactFields(object, names); err != nil {
					return err
				}
			}
		}
	}
	if data, present := raw["ProvisionedThroughput"]; present {
		return dynamoExactFields(data, []string{"ReadCapacityUnits", "WriteCapacityUnits"})
	}
	return nil
}

func (g *Gateway) serveDynamoDB(w http.ResponseWriter, r *http.Request) {
	invalid := func(message string) { dynamoError(w, dynamoFailure{"ValidationException", message}) }
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if r.Method != "POST" || r.URL.Path != "/" || r.URL.RawQuery != "" || mediaErr != nil || mediaType != "application/x-amz-json-1.0" {
		invalid("Use JSON 1.0 at the regional root POST endpoint without query parameters.")
		return
	}
	if service := r.Header.Get("X-Simulator-Service"); service != "" && service != "dynamodb" {
		invalid("Service selection and DynamoDB target disagree.")
		return
	}
	if r.Header.Get("X-Amz-Security-Token") != "" {
		invalid("Session tokens are not supported.")
		return
	}
	region := r.Header.Get("X-Simulator-Region")
	if region == "" {
		region = "us-east-1"
	}
	if a := r.Header.Get("Authorization"); a != "" {
		_, scope, ok := strings.Cut(a, "Credential=")
		scope, _, _ = strings.Cut(scope, ",")
		parts := strings.Split(scope, "/")
		if !ok || !strings.HasPrefix(a, "AWS4-HMAC-SHA256 ") || len(parts) != 5 || !strings.HasPrefix(parts[0], "TCSIM") || parts[3] != "dynamodb" || parts[4] != "aws4_request" {
			dynamoError(w, dynamoFailure{"AccessDeniedException", "Use unsigned requests or dummy TCSIM credentials scoped to DynamoDB; signatures are not authentication."})
			return
		}
		if r.Header.Get("X-Simulator-Region") != "" && region != parts[2] {
			invalid("Region header and credential scope disagree.")
			return
		}
		region = parts[2]
	}
	supported := false
	for _, v := range Regions {
		if region == v {
			supported = true
		}
	}
	if !supported {
		invalid("Unsupported simulator region.")
		return
	}
	op := strings.TrimPrefix(r.Header.Get("X-Amz-Target"), "DynamoDB_20120810.")
	allowed := map[string][]string{
		"CreateTable":   {"TableName", "AttributeDefinitions", "KeySchema", "BillingMode", "ProvisionedThroughput"},
		"ListTables":    {"Limit", "ExclusiveStartTableName"},
		"DescribeTable": {"TableName"},
		"DeleteTable":   {"TableName"},
		"PutItem":       {"TableName", "Item", "ConditionExpression"},
		"GetItem":       {"TableName", "Key", "ConsistentRead"},
		"DeleteItem":    {"TableName", "Key", "ConditionExpression"},
	}
	fields, ok := allowed[op]
	if !ok || r.Header.Get("X-Amz-Target") != "DynamoDB_20120810."+op {
		dynamoError(w, dynamoFailure{"UnknownOperationException", "This DynamoDB operation is not implemented."})
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxDynamoDBRequestBytes))
	if err != nil {
		invalid("DynamoDB requests are limited to 1 MiB.")
		return
	}
	if !utf8.Valid(data) {
		invalid("Request JSON must be UTF-8.")
		return
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err = dynamoUniqueJSON(decoder, 0); err != nil {
		invalid("Expected non-null JSON with unique fields and bounded nesting.")
		return
	}
	if _, err = decoder.Token(); err != io.EOF {
		invalid("Expected exactly one JSON request object.")
		return
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(data, &raw) != nil || raw == nil {
		invalid("Expected one JSON request object.")
		return
	}
	for field := range raw {
		if !slices.Contains(fields, field) {
			invalid("Unsupported request field: " + field)
			return
		}
	}
	if err = dynamoNestedRequestFields(raw); err != nil {
		invalid(err.Error())
		return
	}
	var input dynamoRequest
	if json.Unmarshal(data, &input) != nil {
		invalid("Invalid request field type.")
		return
	}
	if _, present := raw["BillingMode"]; present && input.BillingMode == "" {
		invalid("BillingMode cannot be empty when supplied.")
		return
	}
	if _, present := raw["ConditionExpression"]; present && strings.TrimSpace(input.ConditionExpression) == "" {
		invalid("ConditionExpression cannot be empty when supplied.")
		return
	}
	if op != "ListTables" && !dynamoTableName.MatchString(input.TableName) {
		invalid("TableName must be 3-255 characters of letters, numbers, '_', '-' or '.'.")
		return
	}
	principal := r.Header.Get("X-Simulator-Principal")
	if principal == "" {
		principal = core.Administrator
	}
	authorize := func(resource string) bool {
		decision, err := g.Identities.DecisionContext(r.Context(), principal, "dynamodb:"+op, resource)
		if err != nil {
			dynamoError(w, err)
			return false
		}
		w.Header().Set("X-Simulator-IAM-Decision", decision)
		if decision != "allowed" {
			dynamoError(w, dynamoFailure{"AccessDeniedException", "IAM evaluation: " + decision})
			return false
		}
		return true
	}
	arn := func(name string) string {
		return fmt.Sprintf("arn:aws:dynamodb:%s:000000000000:table/%s", region, name)
	}
	resource := arn(input.TableName)
	if op == "ListTables" {
		resource = "*"
	}
	if !authorize(resource) {
		return
	}
	switch op {
	case "CreateTable":
		g.dynamoCreateTable(w, region, input)
	case "ListTables":
		if _, present := raw["Limit"]; present && (input.Limit < 1 || input.Limit > 100) {
			invalid("Limit must be between 1 and 100.")
			return
		}
		if _, present := raw["ExclusiveStartTableName"]; present && !dynamoTableName.MatchString(input.ExclusiveStartTableName) {
			invalid("Invalid ExclusiveStartTableName.")
			return
		}
		names, last, err := g.DynamoDB.ListTables(region, input.ExclusiveStartTableName, input.Limit)
		if err != nil {
			dynamoError(w, err)
			return
		}
		out := map[string]any{"TableNames": names}
		if last != "" {
			out["LastEvaluatedTableName"] = last
		}
		dynamoJSON(w, 200, out)
	case "DescribeTable", "DeleteTable":
		var t DynamoDBTable
		var err error
		if op == "DescribeTable" {
			t, err = g.DynamoDB.GetTable(region, input.TableName)
		} else {
			t, err = g.DynamoDB.DeleteTable(region, input.TableName)
		}
		if err != nil {
			dynamoError(w, err)
			return
		}
		desc, err := g.dynamoDescription(region, t, op == "DeleteTable")
		if err != nil {
			dynamoError(w, err)
			return
		}
		key := "Table"
		if op == "DeleteTable" {
			key = "TableDescription"
		}
		dynamoJSON(w, 200, map[string]any{key: desc})
	case "PutItem":
		t, err := g.DynamoDB.GetTable(region, input.TableName)
		if err != nil {
			dynamoError(w, err)
			return
		}
		if len(input.Item) == 0 {
			invalid("Item must not be empty.")
			return
		}
		for name, value := range input.Item {
			if err := validateDynamoValue(value, 0); err != nil {
				invalid("Attribute " + name + ": " + err.Error())
				return
			}
		}
		condition, err := parseDynamoCondition(input.ConditionExpression, t.PartitionKey())
		if err != nil {
			dynamoError(w, err)
			return
		}
		pk, sk, err := t.KeyFor(input.Item)
		if err != nil {
			dynamoError(w, err)
			return
		}
		encoded, err := json.Marshal(input.Item)
		if err != nil {
			dynamoError(w, err)
			return
		}
		size, err := dynamoItemSize(input.Item)
		if err != nil {
			invalid(err.Error())
			return
		}
		if size > maxDynamoDBItemBytes {
			invalid("Item size has exceeded the maximum allowed size")
			return
		}
		if err := g.DynamoDB.PutItem(t, pk, sk, string(encoded), condition); err != nil {
			dynamoError(w, err)
			return
		}
		dynamoJSON(w, 200, map[string]any{})
	case "GetItem", "DeleteItem":
		t, err := g.DynamoDB.GetTable(region, input.TableName)
		if err != nil {
			dynamoError(w, err)
			return
		}
		if len(input.Key) != len(t.KeySchema) {
			invalid("Key must contain exactly the attributes declared in the table's key schema.")
			return
		}
		for name := range input.Key {
			if name != t.PartitionKey() && name != t.SortKey() {
				invalid("The provided key element does not match the schema: unexpected attribute " + name)
				return
			}
		}
		pk, sk, err := t.KeyFor(input.Key)
		if err != nil {
			dynamoError(w, err)
			return
		}
		if op == "GetItem" {
			item, found, err := g.DynamoDB.GetItem(t, pk, sk)
			if err != nil {
				dynamoError(w, err)
				return
			}
			out := map[string]any{}
			if found {
				out["Item"] = item
			}
			dynamoJSON(w, 200, out)
			return
		}
		condition, err := parseDynamoCondition(input.ConditionExpression, t.PartitionKey())
		if err != nil {
			dynamoError(w, err)
			return
		}
		if err := g.DynamoDB.DeleteItem(t, pk, sk, condition); err != nil {
			dynamoError(w, err)
			return
		}
		dynamoJSON(w, 200, map[string]any{})
	}
}

func (g *Gateway) dynamoCreateTable(w http.ResponseWriter, region string, input dynamoRequest) {
	invalid := func(message string) { dynamoError(w, dynamoFailure{"ValidationException", message}) }
	definitions := map[string]string{}
	for _, d := range input.AttributeDefinitions {
		if d.AttributeName == "" || (d.AttributeType != "S" && d.AttributeType != "N" && d.AttributeType != "B") {
			invalid("AttributeDefinitions require a name and a type of S, N or B.")
			return
		}
		if _, dup := definitions[d.AttributeName]; dup {
			invalid("Duplicate attribute definition: " + d.AttributeName)
			return
		}
		definitions[d.AttributeName] = d.AttributeType
	}
	var schema []DynamoDBKeyElement
	keyNames := map[string]bool{}
	hash, rangeCount := 0, 0
	for _, k := range input.KeySchema {
		if k.KeyType != "HASH" && k.KeyType != "RANGE" {
			invalid("KeyType must be HASH or RANGE.")
			return
		}
		if _, ok := definitions[k.AttributeName]; !ok {
			invalid("Key attribute " + k.AttributeName + " is not defined in AttributeDefinitions.")
			return
		}
		if keyNames[k.AttributeName] {
			invalid("A key attribute cannot appear more than once in KeySchema.")
			return
		}
		keyNames[k.AttributeName] = true
		if k.KeyType == "HASH" {
			hash++
		} else {
			rangeCount++
		}
		schema = append(schema, DynamoDBKeyElement{k.AttributeName, k.KeyType})
	}
	if hash != 1 || rangeCount > 1 || len(schema) == 0 || len(schema) > 2 {
		invalid("KeySchema requires exactly one HASH key and at most one RANGE key.")
		return
	}
	if len(definitions) != len(schema) {
		invalid("AttributeDefinitions must define exactly the key attributes; non-key attribute definitions are not supported.")
		return
	}
	billing := input.BillingMode
	var readUnits, writeUnits int64
	switch billing {
	case "PAY_PER_REQUEST":
		if input.ProvisionedThroughput != nil {
			invalid("ProvisionedThroughput must not be set with PAY_PER_REQUEST billing.")
			return
		}
	case "PROVISIONED", "":
		if input.ProvisionedThroughput == nil {
			invalid("ProvisionedThroughput is required unless BillingMode is PAY_PER_REQUEST.")
			return
		}
		readUnits, writeUnits = input.ProvisionedThroughput.ReadCapacityUnits, input.ProvisionedThroughput.WriteCapacityUnits
		if readUnits < 1 || writeUnits < 1 {
			invalid("ProvisionedThroughput capacity units must be at least 1.")
			return
		}
		billing = "PROVISIONED"
	default:
		invalid("Unsupported BillingMode: " + billing)
		return
	}
	t, err := g.DynamoDB.CreateTable(DynamoDBTable{Region: region, Name: input.TableName, KeySchema: schema, Definitions: definitions, BillingMode: billing, ReadUnits: readUnits, WriteUnits: writeUnits})
	if err != nil {
		dynamoError(w, err)
		return
	}
	desc, err := g.dynamoDescription(region, t, false)
	if err != nil {
		dynamoError(w, err)
		return
	}
	dynamoJSON(w, 200, map[string]any{"TableDescription": desc})
}

func (g *Gateway) dynamoDescription(region string, t DynamoDBTable, deleting bool) (map[string]any, error) {
	defs := []map[string]any{}
	names := make([]string, 0, len(t.Definitions))
	for n := range t.Definitions {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		defs = append(defs, map[string]any{"AttributeName": n, "AttributeType": t.Definitions[n]})
	}
	keys := []map[string]any{}
	for _, k := range t.KeySchema {
		keys = append(keys, map[string]any{"AttributeName": k.AttributeName, "KeyType": k.KeyType})
	}
	status := "ACTIVE"
	if deleting {
		status = "DELETING"
	}
	desc := map[string]any{
		"TableName":            t.Name,
		"TableStatus":          status,
		"CreationDateTime":     t.Created,
		"TableArn":             fmt.Sprintf("arn:aws:dynamodb:%s:000000000000:table/%s", region, t.Name),
		"TableId":              t.TableID,
		"KeySchema":            keys,
		"AttributeDefinitions": defs,
		"BillingModeSummary":   map[string]any{"BillingMode": t.BillingMode},
	}
	if t.BillingMode == "PROVISIONED" {
		desc["ProvisionedThroughput"] = map[string]any{"ReadCapacityUnits": t.ReadUnits, "WriteCapacityUnits": t.WriteUnits, "NumberOfDecreasesToday": 0}
	}
	var count, size int64
	var err error
	if !deleting {
		count, size, err = g.DynamoDB.TableStats(t)
	}
	if err != nil {
		return nil, err
	}
	desc["ItemCount"] = count
	desc["TableSizeBytes"] = size
	return desc, nil
}
