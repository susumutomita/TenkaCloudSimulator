package aws

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	aspTargetPrefix  = "AnyScaleScalingPlannerFrontendService."
	aspSigningName   = "autoscaling-plans"
	aspRequestBytes  = 256 << 10
	aspTargetLimit   = 2.348542582773833e108 // 2^360, the documented TargetValue bound
	aspPredictiveMsg = "Predictive scaling applies only to EC2 Auto Scaling groups, which this simulator does not provide."
)

var aspStackARN = regexp.MustCompile(`^arn:aws:cloudformation:([a-z0-9-]+):000000000000:stack/[A-Za-z][A-Za-z0-9-]{0,127}/[A-Za-z0-9-]{1,128}$`)

var aspOperations = map[string][]string{
	"CreateScalingPlan":            {"ScalingPlanName", "ApplicationSource", "ScalingInstructions"},
	"UpdateScalingPlan":            {"ScalingPlanName", "ScalingPlanVersion", "ApplicationSource", "ScalingInstructions"},
	"DeleteScalingPlan":            {"ScalingPlanName", "ScalingPlanVersion"},
	"DescribeScalingPlans":         {"ScalingPlanNames", "ScalingPlanVersion", "ApplicationSources", "MaxResults", "NextToken"},
	"DescribeScalingPlanResources": {"ScalingPlanName", "ScalingPlanVersion", "MaxResults", "NextToken"},
}

func aspJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/x-amz-json-1.1")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func aspWriteError(w http.ResponseWriter, err error) {
	code, message, status := "InternalServiceException", "Simulator scaling plan state operation failed.", 500
	var failure aspFailure
	if errors.As(err, &failure) {
		code, message, status = failure.code, failure.message, 400
		switch code {
		case "AccessDeniedException":
			status = 403
		case "UnknownOperationException":
			status = 501
		}
	}
	w.Header().Set("X-Amzn-Errortype", code)
	aspJSON(w, status, map[string]string{"__type": code, "message": message})
}

// aspXMLString applies the official XmlString character class.
func aspXMLString(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, c := range s {
		if !(c == '\t' || c == '\n' || c == '\r' || c >= 0x20 && c <= 0xD7FF || c >= 0xE000 && c <= 0xFFFD || c >= 0x10000 && c <= 0x10FFFF) {
			return false
		}
	}
	return true
}

// aspPlanName applies the official [\p{Print}&&[^|:/]]{1,128} pattern, where
// Java's \p{Print} is printable ASCII.
func aspPlanName(v any, field string) (string, error) {
	name, ok := v.(string)
	if !ok || len(name) < 1 || len(name) > 128 {
		return "", aspInvalid(field + " must be 1 to 128 printable ASCII characters.")
	}
	for i := 0; i < len(name); i++ {
		if name[i] < 0x20 || name[i] > 0x7e || name[i] == '|' || name[i] == ':' || name[i] == '/' {
			return "", aspInvalid(field + " must be printable ASCII without vertical bars, colons or forward slashes.")
		}
	}
	return name, nil
}

func aspObject(v any, what string, allowed ...string) (map[string]any, error) {
	object, ok := v.(map[string]any)
	if !ok {
		return nil, aspInvalid(what + " must be a JSON object.")
	}
	for field := range object {
		if !slices.Contains(allowed, field) {
			return nil, aspInvalid("Unsupported field " + what + "." + field + ".")
		}
	}
	return object, nil
}
func aspList(v any, what string, low, high int) ([]any, error) {
	list, ok := v.([]any)
	if !ok || len(list) < low || len(list) > high {
		return nil, aspInvalid(fmt.Sprintf("%s must be a list of %d to %d entries.", what, low, high))
	}
	return list, nil
}
func aspText(v any, what string, low, high int) (string, error) {
	text, ok := v.(string)
	if !ok || utf8.RuneCountInString(text) < low || utf8.RuneCountInString(text) > high || !aspXMLString(text) {
		return "", aspInvalid(fmt.Sprintf("%s must be a string of %d to %d valid characters.", what, low, high))
	}
	return text, nil
}
func aspInteger(v any, what string, low, high int64) (int64, error) {
	number, ok := v.(json.Number)
	if !ok {
		return 0, aspInvalid(what + " must be an integer.")
	}
	value, err := strconv.ParseInt(string(number), 10, 64)
	if err != nil || value < low || value > high {
		return 0, aspInvalid(fmt.Sprintf("%s must be an integer from %d to %d.", what, low, high))
	}
	return value, nil
}
func aspBool(v any, what string) (*bool, error) {
	value, ok := v.(bool)
	if !ok {
		return nil, aspInvalid(what + " must be a boolean.")
	}
	return &value, nil
}
func aspVersion(v any) error {
	if number, ok := v.(json.Number); !ok || string(number) != "1" {
		return aspInvalid("The only valid ScalingPlanVersion is 1.")
	}
	return nil
}

func aspParseSource(v any, region, what string) (ASPApplicationSource, error) {
	object, err := aspObject(v, what, "CloudFormationStackARN", "TagFilters")
	if err != nil {
		return ASPApplicationSource{}, err
	}
	stack, hasStack := object["CloudFormationStackARN"]
	tags, hasTags := object["TagFilters"]
	if hasStack == hasTags {
		return ASPApplicationSource{}, aspInvalid(what + " requires exactly one of CloudFormationStackARN or TagFilters.")
	}
	if hasStack {
		arn, ok := stack.(string)
		match := aspStackARN.FindStringSubmatch(arn)
		if !ok || match == nil || match[1] != region {
			return ASPApplicationSource{}, aspInvalid(what + ".CloudFormationStackARN must be a stack ARN in this region and the simulator account 000000000000.")
		}
		return ASPApplicationSource{CloudFormationStackARN: arn}, nil
	}
	list, err := aspList(tags, what+".TagFilters", 1, maxAutoScalingTagFilters)
	if err != nil {
		return ASPApplicationSource{}, err
	}
	source := ASPApplicationSource{TagFilters: make([]ASPTagFilter, 0, len(list))}
	keys := map[string]bool{}
	for _, item := range list {
		filter, err := aspObject(item, what+".TagFilters[]", "Key", "Values")
		if err != nil {
			return ASPApplicationSource{}, err
		}
		key, err := aspText(filter["Key"], "TagFilter Key", 1, 128)
		if err != nil {
			return ASPApplicationSource{}, err
		}
		if keys[key] {
			return ASPApplicationSource{}, aspInvalid("TagFilters cannot repeat the key " + key + ".")
		}
		keys[key] = true
		parsed := ASPTagFilter{Key: key}
		if raw, present := filter["Values"]; present {
			values, err := aspList(raw, "TagFilter Values", 0, maxAutoScalingTagValues)
			if err != nil {
				return ASPApplicationSource{}, err
			}
			for _, rawValue := range values {
				value, err := aspText(rawValue, "TagFilter value", 1, 256)
				if err != nil {
					return ASPApplicationSource{}, err
				}
				if slices.Contains(parsed.Values, value) {
					return ASPApplicationSource{}, aspInvalid("A TagFilter cannot repeat a value.")
				}
				parsed.Values = append(parsed.Values, value)
			}
		}
		source.TagFilters = append(source.TagFilters, parsed)
	}
	return source, nil
}

func aspParseTargetTracking(v any, dimension string) (ASPTargetTracking, error) {
	object, err := aspObject(v, "TargetTrackingConfiguration", "PredefinedScalingMetricSpecification", "CustomizedScalingMetricSpecification", "TargetValue", "DisableScaleIn", "ScaleOutCooldown", "ScaleInCooldown", "EstimatedInstanceWarmup")
	if err != nil {
		return ASPTargetTracking{}, err
	}
	if _, present := object["CustomizedScalingMetricSpecification"]; present {
		return ASPTargetTracking{}, aspInvalid("CustomizedScalingMetricSpecification is unsupported; use the predefined DynamoDB utilization metric.")
	}
	if _, present := object["EstimatedInstanceWarmup"]; present {
		return ASPTargetTracking{}, aspInvalid("EstimatedInstanceWarmup applies only to Auto Scaling groups.")
	}
	raw, present := object["PredefinedScalingMetricSpecification"]
	if !present {
		return ASPTargetTracking{}, aspInvalid("TargetTrackingConfiguration requires PredefinedScalingMetricSpecification.")
	}
	metric, err := aspObject(raw, "PredefinedScalingMetricSpecification", "PredefinedScalingMetricType", "ResourceLabel")
	if err != nil {
		return ASPTargetTracking{}, err
	}
	if _, present := metric["ResourceLabel"]; present {
		return ASPTargetTracking{}, aspInvalid("ResourceLabel applies only to ALBRequestCountPerTarget.")
	}
	kind, _ := metric["PredefinedScalingMetricType"].(string)
	expected := autoScalingReadUtilization
	if dimension == autoScalingWriteDimension {
		expected = autoScalingWriteUtilization
	}
	if kind != expected {
		return ASPTargetTracking{}, aspInvalid("PredefinedScalingMetricType for " + dimension + " must be " + expected + ".")
	}
	number, ok := object["TargetValue"].(json.Number)
	if !ok {
		return ASPTargetTracking{}, aspInvalid("TargetValue is required.")
	}
	target, err := strconv.ParseFloat(string(number), 64)
	if err != nil || math.IsNaN(target) || math.IsInf(target, 0) || math.Abs(target) > aspTargetLimit {
		return ASPTargetTracking{}, aspInvalid("TargetValue must be a finite number from -2^360 to 2^360.")
	}
	configuration := ASPTargetTracking{PredefinedScalingMetricSpecification: &ASPPredefinedMetric{PredefinedScalingMetricType: kind}, TargetValue: target}
	if raw, present := object["DisableScaleIn"]; present {
		if configuration.DisableScaleIn, err = aspBool(raw, "DisableScaleIn"); err != nil {
			return ASPTargetTracking{}, err
		}
	}
	for field, out := range map[string]**int64{"ScaleOutCooldown": &configuration.ScaleOutCooldown, "ScaleInCooldown": &configuration.ScaleInCooldown} {
		if raw, present := object[field]; present {
			value, err := aspInteger(raw, field, 0, math.MaxInt32)
			if err != nil {
				return ASPTargetTracking{}, err
			}
			*out = &value
		}
	}
	return configuration, nil
}

func aspParseInstruction(v any) (ASPInstruction, error) {
	object, ok := v.(map[string]any)
	if !ok {
		return ASPInstruction{}, aspInvalid("Each ScalingInstruction must be a JSON object.")
	}
	for _, field := range []string{"PredefinedLoadMetricSpecification", "CustomizedLoadMetricSpecification", "ScheduledActionBufferTime", "PredictiveScalingMaxCapacityBehavior", "PredictiveScalingMaxCapacityBuffer", "PredictiveScalingMode"} {
		if _, present := object[field]; present {
			return ASPInstruction{}, aspInvalid(field + ": " + aspPredictiveMsg)
		}
	}
	if _, err := aspObject(v, "ScalingInstruction", "ServiceNamespace", "ResourceId", "ScalableDimension", "MinCapacity", "MaxCapacity", "TargetTrackingConfigurations", "ScalingPolicyUpdateBehavior", "DisableDynamicScaling"); err != nil {
		return ASPInstruction{}, err
	}
	namespace, _ := object["ServiceNamespace"].(string)
	if !slices.Contains([]string{"autoscaling", "ecs", "ec2", "rds", "dynamodb"}, namespace) {
		return ASPInstruction{}, aspInvalid("ServiceNamespace must be one of autoscaling, ecs, ec2, rds or dynamodb.")
	}
	if namespace != autoScalingNamespaceDynamoDB {
		return ASPInstruction{}, aspInvalid("Only DynamoDB table targets are supported by this simulator; " + namespace + " resources are not provided.")
	}
	resourceID, _ := object["ResourceId"].(string)
	name, isTable := strings.CutPrefix(resourceID, "table/")
	if isTable && strings.Contains(name, "/index/") {
		return ASPInstruction{}, aspInvalid("DynamoDB global secondary index targets are unsupported because the simulator has no indexes.")
	}
	if !isTable || !dynamoTableName.MatchString(name) {
		return ASPInstruction{}, aspInvalid("ResourceId must be table/<table-name> for a DynamoDB table.")
	}
	dimension, _ := object["ScalableDimension"].(string)
	if dimension != autoScalingReadDimension && dimension != autoScalingWriteDimension {
		return ASPInstruction{}, aspInvalid("ScalableDimension must be dynamodb:table:ReadCapacityUnits or dynamodb:table:WriteCapacityUnits.")
	}
	instruction := ASPInstruction{ServiceNamespace: namespace, ResourceID: resourceID, ScalableDimension: dimension, TargetTrackingConfigurations: []ASPTargetTracking{}}
	var err error
	if instruction.MinCapacity, err = aspInteger(object["MinCapacity"], "MinCapacity", 1, maxAutoScalingCapacity); err != nil {
		return ASPInstruction{}, err
	}
	if instruction.MaxCapacity, err = aspInteger(object["MaxCapacity"], "MaxCapacity", 1, maxAutoScalingCapacity); err != nil {
		return ASPInstruction{}, err
	}
	if instruction.MaxCapacity < instruction.MinCapacity {
		return ASPInstruction{}, aspInvalid("MaxCapacity must be greater than or equal to MinCapacity.")
	}
	if raw, present := object["DisableDynamicScaling"]; present {
		if instruction.DisableDynamicScaling, err = aspBool(raw, "DisableDynamicScaling"); err != nil {
			return ASPInstruction{}, err
		}
	}
	if raw, present := object["ScalingPolicyUpdateBehavior"]; present {
		behavior, _ := raw.(string)
		if behavior != "KeepExternalPolicies" && behavior != "ReplaceExternalPolicies" {
			return ASPInstruction{}, aspInvalid("ScalingPolicyUpdateBehavior must be KeepExternalPolicies or ReplaceExternalPolicies.")
		}
		instruction.ScalingPolicyUpdateBehavior = behavior
	}
	low := 1
	if instruction.DisableDynamicScaling != nil && *instruction.DisableDynamicScaling {
		low = 0
	}
	configurations, err := aspList(object["TargetTrackingConfigurations"], "TargetTrackingConfigurations", low, maxAutoScalingTargetTrackings)
	if err != nil {
		return ASPInstruction{}, err
	}
	for _, raw := range configurations {
		configuration, err := aspParseTargetTracking(raw, dimension)
		if err != nil {
			return ASPInstruction{}, err
		}
		if len(instruction.TargetTrackingConfigurations) > 0 {
			return ASPInstruction{}, aspInvalid("Each target tracking configuration must use a unique scaling metric.")
		}
		instruction.TargetTrackingConfigurations = append(instruction.TargetTrackingConfigurations, configuration)
	}
	return instruction, nil
}

func aspParseInstructions(v any) ([]ASPInstruction, error) {
	list, err := aspList(v, "ScalingInstructions", 1, maxAutoScalingInstructions)
	if err != nil {
		return nil, err
	}
	instructions := make([]ASPInstruction, 0, len(list))
	seen := map[string]bool{}
	for _, raw := range list {
		instruction, err := aspParseInstruction(raw)
		if err != nil {
			return nil, err
		}
		key := instruction.ResourceID + "\x00" + instruction.ScalableDimension
		if seen[key] {
			return nil, aspInvalid("ScalingInstructions cannot repeat " + instruction.ResourceID + " " + instruction.ScalableDimension + ".")
		}
		seen[key] = true
		instructions = append(instructions, instruction)
	}
	return instructions, nil
}

func aspPageSize(input map[string]any) (int, error) {
	raw, present := input["MaxResults"]
	if !present {
		return maxAutoScalingPageSize, nil
	}
	value, err := aspInteger(raw, "MaxResults", 1, maxAutoScalingPageSize)
	return int(value), err
}
func aspNextToken(input map[string]any) (string, error) {
	raw, present := input["NextToken"]
	if !present {
		return "", nil
	}
	token, ok := raw.(string)
	if !ok || token == "" {
		return "", aspFailure{"InvalidNextTokenException", "The NextToken is invalid for this request."}
	}
	return token, nil
}

// ServeHTTP handles the bounded Auto Scaling Plans JSON 1.1 operations. The
// trusted Gateway supplies the region and one immutable caller authority;
// server middleware remains responsible for HTTP and team authentication.
// Every authorization decision completes before state is read or written.
func (s *AutoScalingPlansStore) ServeHTTP(w http.ResponseWriter, r *http.Request, region string, authorize func(action, resource string) error) {
	fail := func(err error) { aspWriteError(w, err) }
	mediaType, _, mediaErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if r.Method != "POST" || r.URL.Path != "/" || r.URL.RawQuery != "" || mediaErr != nil || mediaType != "application/x-amz-json-1.1" {
		fail(aspInvalid("Use JSON 1.1 root POST without query parameters."))
		return
	}
	if !slices.Contains(Regions, region) {
		fail(aspInvalid("Unsupported simulator region."))
		return
	}
	if selected := r.Header.Get("X-Simulator-Service"); selected != "" && selected != aspSigningName {
		fail(aspInvalid("Service selection and Auto Scaling Plans target disagree."))
		return
	}
	if r.Header.Get("X-Amz-Security-Token") != "" {
		fail(aspInvalid("Session tokens are unsupported."))
		return
	}
	if a := r.Header.Get("Authorization"); a != "" {
		_, scope, ok := strings.Cut(a, "Credential=")
		scope, _, _ = strings.Cut(scope, ",")
		parts := strings.Split(scope, "/")
		if !ok || !strings.HasPrefix(a, "AWS4-HMAC-SHA256 ") || len(parts) != 5 || !strings.HasPrefix(parts[0], "TCSIM") || parts[2] != region || parts[3] != aspSigningName || parts[4] != "aws4_request" {
			fail(aspFailure{"AccessDeniedException", "Use dummy TCSIM credentials scoped to this region and autoscaling-plans; signatures are not real AWS authentication."})
			return
		}
	}
	target := r.Header.Get("X-Amz-Target")
	op, hasPrefix := strings.CutPrefix(target, aspTargetPrefix)
	fields, known := aspOperations[op]
	if !hasPrefix || !known {
		message := "This Auto Scaling Plans operation is not implemented."
		if hasPrefix && op == "GetScalingPlanResourceForecastData" {
			message = "GetScalingPlanResourceForecastData requires EC2 Auto Scaling groups and CloudWatch load history, which this simulator does not provide."
		}
		fail(aspFailure{"UnknownOperationException", message})
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, aspRequestBytes))
	if err != nil {
		fail(aspInvalid("Request JSON exceeds the 256 KiB simulator transport limit."))
		return
	}
	if !utf8.Valid(data) {
		fail(aspInvalid("Request JSON must be UTF-8."))
		return
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	if err = logsUniqueJSON(dec, 0); err != nil {
		fail(aspInvalid("Expected one JSON object with unique, non-null fields."))
		return
	}
	if _, err = dec.Token(); err != io.EOF {
		fail(aspInvalid("Expected exactly one JSON object."))
		return
	}
	dec = json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var body any
	if err = dec.Decode(&body); err != nil {
		fail(aspInvalid("Invalid request JSON."))
		return
	}
	input, err := aspObject(body, "request", fields...)
	if err != nil {
		fail(err)
		return
	}
	if authorize == nil {
		fail(errors.New("missing Auto Scaling Plans authorizer"))
		return
	}
	ctx := r.Context()
	var output any = map[string]any{}
	switch op {
	case "CreateScalingPlan", "UpdateScalingPlan":
		name, err := aspPlanName(input["ScalingPlanName"], "ScalingPlanName")
		if err != nil {
			fail(err)
			return
		}
		var source *ASPApplicationSource
		var instructions []ASPInstruction
		if op == "UpdateScalingPlan" {
			if err = aspVersion(input["ScalingPlanVersion"]); err != nil {
				fail(err)
				return
			}
		}
		if raw, present := input["ApplicationSource"]; present || op == "CreateScalingPlan" {
			parsed, err := aspParseSource(raw, region, "ApplicationSource")
			if err != nil {
				fail(err)
				return
			}
			source = &parsed
		}
		if raw, present := input["ScalingInstructions"]; present || op == "CreateScalingPlan" {
			if instructions, err = aspParseInstructions(raw); err != nil {
				fail(err)
				return
			}
		}
		if err = authorize("autoscaling-plans:"+op, "*"); err != nil {
			fail(err)
			return
		}
		// No service-linked role exists locally: the caller's own authority
		// must cover every DynamoDB table whose capacity the plan bounds.
		for _, instruction := range instructions {
			table := fmt.Sprintf("arn:aws:dynamodb:%s:000000000000:table/%s", region, aspTableName(instruction.ResourceID))
			for _, action := range []string{"dynamodb:DescribeTable", "dynamodb:UpdateTable"} {
				if err = authorize(action, table); err != nil {
					fail(err)
					return
				}
			}
		}
		if op == "CreateScalingPlan" {
			err = s.Create(ctx, region, name, *source, instructions)
			output = map[string]any{"ScalingPlanVersion": autoScalingPlanVersion}
		} else {
			err = s.Update(ctx, region, name, source, instructions)
		}
		if err != nil {
			fail(err)
			return
		}
	case "DeleteScalingPlan":
		name, err := aspPlanName(input["ScalingPlanName"], "ScalingPlanName")
		if err == nil {
			err = aspVersion(input["ScalingPlanVersion"])
		}
		if err == nil {
			err = authorize("autoscaling-plans:"+op, "*")
		}
		if err != nil {
			fail(err)
			return
		}
		if err = s.Delete(ctx, region, name); err != nil {
			fail(err)
			return
		}
	case "DescribeScalingPlans":
		query := ASPPlanQuery{}
		if raw, present := input["ScalingPlanNames"]; present {
			names, err := aspList(raw, "ScalingPlanNames", 1, 10)
			if err != nil {
				fail(err)
				return
			}
			for _, value := range names {
				name, err := aspPlanName(value, "ScalingPlanNames[]")
				if err != nil {
					fail(err)
					return
				}
				query.Names = append(query.Names, name)
			}
		}
		if raw, present := input["ApplicationSources"]; present {
			if query.Names != nil {
				fail(aspInvalid("ScalingPlanNames and ApplicationSources cannot be combined."))
				return
			}
			sources, err := aspList(raw, "ApplicationSources", 1, 10)
			if err != nil {
				fail(err)
				return
			}
			for _, value := range sources {
				source, err := aspParseSource(value, region, "ApplicationSources[]")
				if err != nil {
					fail(err)
					return
				}
				query.Sources = append(query.Sources, source)
			}
		}
		if raw, present := input["ScalingPlanVersion"]; present {
			if query.Names == nil {
				fail(aspInvalid("ScalingPlanVersion requires ScalingPlanNames."))
				return
			}
			if err = aspVersion(raw); err != nil {
				fail(err)
				return
			}
		}
		if query.Limit, err = aspPageSize(input); err == nil {
			query.Token, err = aspNextToken(input)
		}
		if err == nil {
			err = authorize("autoscaling-plans:"+op, "*")
		}
		if err != nil {
			fail(err)
			return
		}
		plans, next, err := s.Plans(ctx, region, query)
		if err != nil {
			fail(err)
			return
		}
		result := map[string]any{"ScalingPlans": plans}
		if next != "" {
			result["NextToken"] = next
		}
		output = result
	case "DescribeScalingPlanResources":
		name, err := aspPlanName(input["ScalingPlanName"], "ScalingPlanName")
		if err == nil {
			err = aspVersion(input["ScalingPlanVersion"])
		}
		limit, token := 0, ""
		if err == nil {
			limit, err = aspPageSize(input)
		}
		if err == nil {
			token, err = aspNextToken(input)
		}
		if err == nil {
			err = authorize("autoscaling-plans:"+op, "*")
		}
		if err != nil {
			fail(err)
			return
		}
		resources, next, err := s.Resources(ctx, region, name, token, limit)
		if err != nil {
			fail(err)
			return
		}
		result := map[string]any{"ScalingPlanResources": resources}
		if next != "" {
			result["NextToken"] = next
		}
		output = result
	}
	aspJSON(w, 200, output)
}
