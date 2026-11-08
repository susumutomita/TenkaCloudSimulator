package aws

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

type stackParameterResolver func(string, map[string]string) (map[string]string, error)

func stackSSMValueType(kind string) bool {
	return kind == "AWS::SSM::Parameter::Value<String>" || kind == "AWS::SSM::Parameter::Value<AWS::EC2::Image::Id>"
}

// Public /aws keys are valid read names, but are never synthesized or fetched
// from AWS. Native PutParameter keeps its existing reserved-prefix restrictions.
func stackSSMKey(name string) bool {
	return len(name) <= 1011 && parameterName.MatchString(name) && strings.Count(name, "/") <= 15
}

// ResolvedValue is a Query/XML field. Refuse unrepresentable data rather than
// silently replacing bytes when the saved snapshot is described.
func stackSSMString(value string) bool {
	if len(value) == 0 || len(value) > 4096 || !utf8.ValidString(value) {
		return false
	}
	for _, c := range value {
		if c != '\t' && c != '\n' && c != '\r' && (c < 0x20 || c == 0xfffe || c == 0xffff) {
			return false
		}
	}
	return true
}

func stackSSMRequests(definitions map[string]any, values map[string]string) map[string]string {
	keys := map[string]string{}
	for name, raw := range definitions {
		if stackSSMValueType(stringValue(object(raw)["Type"])) {
			keys[name] = values[name]
		}
	}
	return keys
}

// Authorize every source before opening the read transaction. Read exactly the
// requested region/name rows in one snapshot, then release it before native
// creation. Later source changes do not change the compiled/saved stack values.
func (s *StackStore) resolveSSMParameters(ctx context.Context, region string, keys map[string]string, authorize func(string, string) error) (map[string]string, error) {
	names := make([]string, 0, len(keys))
	for logical := range keys {
		names = append(names, logical)
	}
	sort.Strings(names)
	for _, logical := range names {
		key := keys[logical]
		if !stackSSMKey(key) {
			return nil, ssmFailure{"ValidationError", "SSM parameter requires an unversioned local name."}
		}
		arn := fmt.Sprintf("arn:aws:ssm:%s:000000000000:parameter/%s", region, trimParameterSlash(key))
		if err := authorize("ssm:GetParameters", arn); err != nil {
			return nil, err
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	values := map[string]string{}
	for _, logical := range names {
		var kind, value string
		err = tx.QueryRowContext(ctx, "SELECT type,value FROM aws_ssm_parameters WHERE region=? AND name=?", region, keys[logical]).Scan(&kind, &value)
		if err == sql.ErrNoRows {
			return nil, ssmFailure{"ValidationError", "A referenced local SSM parameter does not exist."}
		}
		if err != nil {
			return nil, err
		}
		if kind != "String" || !stackSSMString(value) {
			return nil, ssmFailure{"ValidationError", "SSM value parameters require bounded local Standard String data."}
		}
		values[logical] = value
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return values, nil
}

// Existing ordinary parameter output behavior is unchanged. Only this new
// typed subset returns ParameterValue/ResolvedValue; NoEcho=true is refused
// for these definitions because this simulator does not provide secret storage.
func stackSSMParametersXML(record StackRecord) string {
	keys := make([]string, 0, len(record.ResolvedParameters))
	for key := range record.ResolvedParameters {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var out strings.Builder
	out.WriteString("<Parameters>")
	for _, key := range keys {
		out.WriteString("<member><ParameterKey>" + xmlText(key) + "</ParameterKey><ParameterValue>" + xmlText(record.Parameters[key]) + "</ParameterValue><ResolvedValue>" + xmlText(record.ResolvedParameters[key]) + "</ResolvedValue></member>")
	}
	out.WriteString("</Parameters>")
	return out.String()
}
