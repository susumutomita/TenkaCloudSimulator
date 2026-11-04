package aws

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	pythonruntime "github.com/susumutomita/TenkaCloudSimulator/internal/workload/python"
)

type LambdaFunction struct {
	Name, Region, Role, Handler, Description, Modified, Revision string
	Timeout                                                      int
	Code                                                         []byte
	Environment                                                  map[string]string
	Tags                                                         map[string]string
}

type LambdaStore struct {
	db                  *sql.DB
	mu                  sync.Mutex
	runtime             *pythonruntime.Runtime
	closed              bool
	context             context.Context
	cancel              context.CancelFunc
	initialization      context.Context
	initialized         chan struct{}
	initializationError error
}

func NewLambdaStore(db *sql.DB) (*LambdaStore, error) {
	if _, err := NewIAMRoleStore(db); err != nil {
		return nil, err
	}
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS aws_lambda_functions(region TEXT NOT NULL,name TEXT NOT NULL,document TEXT NOT NULL,code_bytes INTEGER NOT NULL,PRIMARY KEY(region,name));`)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &LambdaStore{db: db, context: ctx, cancel: cancel}, nil
}
func (s *LambdaStore) Close() error {
	s.cancel()
	s.mu.Lock()
	s.closed = true
	runtime := s.runtime
	s.mu.Unlock()
	if runtime != nil {
		return runtime.Close(context.Background())
	}
	return nil
}
func (s *LambdaStore) runner(request context.Context) (*pythonruntime.Runtime, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, errors.New("Lambda runtime closed")
	}
	if s.runtime != nil {
		runtime := s.runtime
		s.mu.Unlock()
		return runtime, nil
	}
	if s.initialized == nil {
		ctx, cancel := context.WithTimeout(s.context, 2*time.Minute)
		s.initialization = ctx
		s.initialized = make(chan struct{})
		go s.initialize(ctx, cancel)
	}
	done, initialization := s.initialized, s.initialization
	s.mu.Unlock()
	select {
	case <-request.Done():
		return nil, request.Err()
	case <-initialization.Done():
	case <-done:
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("Lambda runtime closed")
	}
	if s.runtime != nil {
		return s.runtime, nil
	}
	if s.initializationError != nil {
		return nil, s.initializationError
	}
	return nil, initialization.Err()
}
func (s *LambdaStore) initialize(ctx context.Context, cancel context.CancelFunc) {
	defer cancel()
	runtime, err := pythonruntime.New(ctx)
	s.mu.Lock()
	if s.closed || ctx.Err() != nil {
		if runtime != nil {
			_ = runtime.Close(context.Background())
		}
		if err == nil {
			err = context.Canceled
		}
		runtime = nil
	}
	s.runtime, s.initializationError = runtime, err
	close(s.initialized)
	s.mu.Unlock()
}
func (s *LambdaStore) get(region, name string) (LambdaFunction, error) {
	var data []byte
	var f LambdaFunction
	err := s.db.QueryRow("SELECT document FROM aws_lambda_functions WHERE region=? AND name=?", region, name).Scan(&data)
	if err == sql.ErrNoRows {
		return f, ssmFailure{"ResourceNotFoundException", "Function not found."}
	}
	if err != nil {
		return f, err
	}
	err = json.Unmarshal(data, &f)
	return f, err
}
func (s *LambdaStore) save(f *LambdaFunction, create bool, revision string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = saveLambdaFunction(tx, f, create, revision); err != nil {
		return err
	}
	return tx.Commit()
}

// Shared by the native API and transactional CloudFormation creation.
func saveLambdaFunction(tx *sql.Tx, f *LambdaFunction, create bool, revision string) error {
	var err error
	var count, total int
	if err = tx.QueryRow("SELECT count(*),coalesce(sum(code_bytes),0) FROM aws_lambda_functions").Scan(&count, &total); err != nil {
		return err
	}
	var previous []byte
	err = tx.QueryRow("SELECT document FROM aws_lambda_functions WHERE region=? AND name=?", f.Region, f.Name).Scan(&previous)
	if create && err == nil {
		return ssmFailure{"ResourceConflictException", "Function already exists."}
	}
	if !create && err == sql.ErrNoRows {
		return ssmFailure{"ResourceNotFoundException", "Function not found."}
	}
	if err != nil && err != sql.ErrNoRows {
		return err
	}
	if !create {
		var old LambdaFunction
		if err = json.Unmarshal(previous, &old); err != nil {
			return err
		}
		if revision != "" && revision != old.Revision {
			return ssmFailure{"PreconditionFailedException", "Function revision changed."}
		}
		total -= len(old.Code)
		old.Code = f.Code
		old.Modified = f.Modified
		old.Revision = f.Revision
		*f = old
	}
	if create && count >= 100 || total+len(f.Code) > 8<<20 {
		return ssmFailure{"TooManyRequestsException", "Simulator function count or total code quota exceeded."}
	}
	var role int
	name := strings.TrimPrefix(f.Role, "arn:aws:iam::000000000000:role/")
	err = tx.QueryRow("SELECT (SELECT count(*) FROM identity_policies WHERE name=?)+(SELECT count(*) FROM aws_iam_roles WHERE name=? AND deleted=0)", name, name).Scan(&role)
	if err != nil {
		return err
	}
	if role == 0 {
		return ssmFailure{"InvalidParameterValueException", "Execution role must be an existing local identity."}
	}
	if err = iamExecutionRole(context.Background(), tx, f.Role); err != nil {
		return err
	}
	// This concrete record contains only strings, integers, byte slices and
	// string maps; it cannot contain an unsupported JSON value.
	data, _ := json.Marshal(f)
	if create {
		_, err = tx.Exec("INSERT INTO aws_lambda_functions VALUES(?,?,?,?)", f.Region, f.Name, string(data), len(f.Code))
	} else {
		_, err = tx.Exec("UPDATE aws_lambda_functions SET document=?,code_bytes=? WHERE region=? AND name=?", string(data), len(f.Code), f.Region, f.Name)
	}
	if err != nil {
		return err
	}
	return nil
}

var lambdaName = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
var localRole = regexp.MustCompile(`^arn:aws:iam::000000000000:role/[a-zA-Z0-9_-]{1,64}$`)

func lambdaARN(region, name string) string {
	return "arn:aws:lambda:" + region + ":000000000000:function:" + name
}
func lambdaConfiguration(f LambdaFunction) map[string]any {
	hash := sha256.Sum256(f.Code)
	return map[string]any{"FunctionName": f.Name, "FunctionArn": lambdaARN(f.Region, f.Name), "Runtime": "python3.12", "Role": f.Role, "Handler": f.Handler, "CodeSize": len(f.Code), "Description": f.Description, "Timeout": f.Timeout, "MemorySize": 128, "LastModified": f.Modified, "CodeSha256": base64.StdEncoding.EncodeToString(hash[:]), "Version": "$LATEST", "RevisionId": f.Revision, "State": "Active", "LastUpdateStatus": "Successful", "PackageType": "Zip", "Environment": map[string]any{"Variables": f.Environment}}
}
func lambdaJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if value != nil {
		_ = json.NewEncoder(w).Encode(value)
	}
}
func lambdaError(w http.ResponseWriter, err error) {
	code, message, status := "ServiceException", "Simulator function operation failed.", 500
	var e ssmFailure
	if errors.As(err, &e) {
		code, message, status = e.code, e.message, 400
		switch code {
		case "AccessDeniedException":
			status = 403
		case "ResourceNotFoundException":
			status = 404
		case "ResourceConflictException":
			status = 409
		case "PreconditionFailedException":
			status = 412
		case "TooManyRequestsException":
			status = 429
		case "NotImplementedException":
			status = 501
		}
	}
	w.Header().Set("X-Amzn-ErrorType", code)
	lambdaJSON(w, status, map[string]string{"Type": "User", "message": message})
}
func lambdaRegion(r *http.Request) (string, error) {
	region := r.Header.Get("X-Simulator-Region")
	auth := r.Header.Get("Authorization")
	if auth != "" {
		_, scope, ok := strings.Cut(auth, "Credential=")
		parts := strings.Split(scope, "/")
		if !ok || len(parts) < 5 || !strings.HasPrefix(parts[0], "TCSIM") || parts[3] != "lambda" {
			return "", ssmFailure{"AccessDeniedException", "Use dummy TCSIM Lambda credentials."}
		}
		if region != "" && region != parts[2] {
			return "", ssmFailure{"InvalidParameterValueException", "Region header and credential scope disagree."}
		}
		region = parts[2]
	}
	if r.Header.Get("X-Amz-Security-Token") != "" {
		return "", ssmFailure{"NotImplementedException", "Session tokens are unsupported."}
	}
	if region == "" {
		region = "us-east-1"
	}
	for _, v := range Regions {
		if v == region {
			return region, nil
		}
	}
	return "", ssmFailure{"InvalidParameterValueException", "Unsupported simulator region."}
}
func (g *Gateway) serveLambda(w http.ResponseWriter, r *http.Request) {
	invalid := func(message string) { lambdaError(w, ssmFailure{"InvalidParameterValueException", message}) }
	if g.Lambdas == nil {
		lambdaError(w, ssmFailure{"NotImplementedException", "Lambda state is not configured."})
		return
	}
	if service := r.Header.Get("X-Simulator-Service"); service != "" && service != "lambda" {
		invalid("Service header and Lambda credential scope disagree.")
		return
	}
	region, err := lambdaRegion(r)
	if err != nil {
		lambdaError(w, err)
		return
	}
	if r.Header.Get("X-Amz-Target") != "" {
		invalid("Lambda REST requests must not contain X-Amz-Target.")
		return
	}
	path := strings.TrimSuffix(r.URL.Path, "/")
	const prefix = "/2015-03-31/functions"
	if path != prefix && !strings.HasPrefix(path, prefix+"/") {
		lambdaError(w, ssmFailure{"NotImplementedException", "Unsupported Lambda REST path."})
		return
	}
	parts := []string{}
	if path != prefix {
		parts = strings.Split(strings.TrimPrefix(path, prefix+"/"), "/")
	}
	if len(parts) > 3 || len(parts) == 3 && parts[1] != "policy" {
		invalid("Unsupported function resource path.")
		return
	}
	name := ""
	if len(parts) > 0 {
		name = parts[0]
		arnPrefix := "arn:aws:lambda:" + region + ":000000000000:function:"
		if strings.HasPrefix(name, arnPrefix) {
			name = strings.TrimPrefix(name, arnPrefix)
		}
		if !lambdaName.MatchString(name) {
			invalid("Use an unqualified local function name or ARN.")
			return
		}
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		invalid("Malformed Lambda query string.")
		return
	}
	for key, values := range query {
		if len(values) != 1 {
			invalid("Duplicate query fields are unsupported.")
			return
		}
		switch key {
		case "Qualifier":
			if values[0] != "$LATEST" || len(parts) != 2 || !(parts[1] == "configuration" && r.Method == "GET" || parts[1] == "invocations" && r.Method == "POST") {
				lambdaError(w, ssmFailure{"NotImplementedException", "Versions and aliases are unsupported."})
				return
			}
		case "RevisionId":
			if len(parts) != 3 || parts[1] != "policy" || r.Method != "DELETE" {
				invalid("Resource policy revision is only valid on RemovePermission.")
				return
			}
		case "MaxItems", "Marker":
			if name != "" || r.Method != "GET" {
				invalid("Pagination is only valid on ListFunctions.")
				return
			}
		default:
			invalid("Unsupported query field: " + key)
			return
		}
	}
	principal := r.Header.Get("X-Simulator-Principal")
	if principal == "" {
		principal = core.Administrator
	}
	// One immutable policy snapshot covers both CreateFunction and PassRole.
	var policy core.Policy
	if principal != core.Administrator {
		policy, _, err = g.Identities.Policy(principal)
		if err != nil {
			lambdaError(w, err)
			return
		}
	}
	authorize := func(action, resource string) bool {
		decision := "allowed"
		if principal != core.Administrator {
			decision = core.Evaluate(policy, action, resource)
		}
		w.Header().Set("X-Simulator-IAM-Decision", decision)
		if decision != "allowed" {
			lambdaError(w, ssmFailure{"AccessDeniedException", "IAM evaluation: " + decision})
			return false
		}
		return true
	}
	if len(parts) >= 2 && parts[1] == "policy" {
		g.serveLambdaPolicy(w, r, region, name, parts, authorize)
		return
	}
	if name == "" && r.Method == "GET" {
		if r.URL.Query().Has("Qualifier") {
			invalid("Qualifier is not valid on ListFunctions.")
			return
		}
		if !authorize("lambda:ListFunctions", "*") {
			return
		}
		max := 50
		if v := r.URL.Query().Get("MaxItems"); v != "" {
			max, err = strconv.Atoi(v)
			if err != nil || max < 1 || max > 10000 {
				invalid("MaxItems must be 1–10000.")
				return
			}
			max = min(max, 50)
		}
		marker := r.URL.Query().Get("Marker")
		if marker != "" && !lambdaName.MatchString(marker) {
			invalid("Invalid function marker.")
			return
		}
		rows, e := g.Lambdas.db.Query("SELECT document FROM aws_lambda_functions WHERE region=? AND name>? ORDER BY name LIMIT ?", region, marker, max+1)
		if e != nil {
			lambdaError(w, e)
			return
		}
		functions := []LambdaFunction{}
		for rows.Next() {
			var data []byte
			var f LambdaFunction
			if e = rows.Scan(&data); e == nil {
				e = json.Unmarshal(data, &f)
			}
			if e != nil {
				break
			}
			functions = append(functions, f)
		}
		if e == nil {
			e = rows.Err()
		}
		_ = rows.Close()
		if e != nil {
			lambdaError(w, e)
			return
		}
		result := map[string]any{}
		if len(functions) > max {
			result["NextMarker"] = functions[max-1].Name
			functions = functions[:max]
		}
		list := []any{}
		for _, f := range functions {
			list = append(list, lambdaConfiguration(f))
		}
		result["Functions"] = list
		lambdaJSON(w, 200, result)
		return
	}
	creating := name == "" && r.Method == "POST"
	updating := len(parts) == 2 && parts[1] == "code" && r.Method == "PUT"
	if creating || updating {
		var input struct {
			FunctionName string
			Runtime      string
			Role         string
			Handler      string
			Description  string
			Timeout      *int
			MemorySize   *int
			PackageType  string
			Code         struct{ ZipFile []byte }
			ZipFile      []byte
			Environment  *struct{ Variables map[string]string }
			Tags         map[string]string
			RevisionId   string
		}
		data, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 384<<10))
		if e != nil {
			invalid("Function request exceeds 384 KiB.")
			return
		}
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&input) != nil || decoder.Decode(new(any)) != io.EOF {
			invalid("Use one supported JSON function request.")
			return
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(data, &fields) != nil || fields == nil {
			invalid("Expected a function request object.")
			return
		}
		allowed := map[string]bool{"ZipFile": true, "RevisionId": true}
		if creating {
			allowed = map[string]bool{"FunctionName": true, "Runtime": true, "Role": true, "Handler": true, "Description": true, "Timeout": true, "MemorySize": true, "PackageType": true, "Code": true, "Environment": true, "Tags": true}
		}
		for key, value := range fields {
			if !allowed[key] || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				invalid("Unsupported or null function field: " + key)
				return
			}
		}
		var f LambdaFunction
		if creating {
			name = input.FunctionName
			if !lambdaName.MatchString(name) || input.Runtime != "python3.12" || !localRole.MatchString(input.Role) || input.PackageType != "" && input.PackageType != "Zip" || input.MemorySize != nil && *input.MemorySize != 128 || len(input.Description) > 256 || input.RevisionId != "" || input.ZipFile != nil {
				invalid("Use a named Python 3.12 ZIP function, a local execution-role ARN and 128 MiB memory.")
				return
			}
			if !authorize("lambda:CreateFunction", lambdaARN(region, name)) || !authorize("iam:PassRole", input.Role) {
				return
			}
			if len(input.Tags) > 0 && !authorize("lambda:TagResource", lambdaARN(region, name)) {
				return
			}
			timeout := 3
			if input.Timeout != nil {
				timeout = *input.Timeout
			}
			if timeout < 1 || timeout > 30 {
				invalid("Timeout must be 1–30 seconds.")
				return
			}
			f = LambdaFunction{Name: name, Region: region, Role: input.Role, Handler: input.Handler, Description: input.Description, Timeout: timeout, Code: input.Code.ZipFile, Environment: map[string]string{}, Tags: input.Tags}
			if input.Environment != nil {
				f.Environment = input.Environment.Variables
			}
		} else {
			if !authorize("lambda:UpdateFunctionCode", lambdaARN(region, name)) {
				return
			}
			f, e = g.Lambdas.get(region, name)
			if e != nil {
				lambdaError(w, e)
				return
			}
			f.Code = input.ZipFile
		}
		if _, e = pythonruntime.ValidateCode(f.Code, f.Handler); e != nil {
			invalid(e.Error())
			return
		}
		environmentBytes := 0
		for key, value := range f.Environment {
			environmentBytes += len(key) + len(value)
			if !regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]+$`).MatchString(key) || strings.ContainsRune(value, '\x00') {
				invalid("Invalid environment key/value.")
				return
			}
		}
		if len(f.Environment) > 64 || environmentBytes > 4096 || len(f.Tags) > 50 {
			invalid("Environment or tags exceed local limits.")
			return
		}
		for key, value := range f.Tags {
			if len(key) == 0 || len(key) > 128 || len(value) > 256 {
				invalid("Invalid function tag.")
				return
			}
		}
		var revision [16]byte
		if _, e = rand.Read(revision[:]); e != nil {
			lambdaError(w, e)
			return
		}
		f.Revision = hex.EncodeToString(revision[:])
		f.Modified = time.Now().UTC().Format("2006-01-02T15:04:05.000+0000")
		if e = g.Lambdas.save(&f, creating, input.RevisionId); e != nil {
			lambdaError(w, e)
			return
		}
		status := 200
		if creating {
			status = 201
		}
		lambdaJSON(w, status, lambdaConfiguration(f))
		return
	}
	if len(parts) == 1 && r.Method == "DELETE" {
		if !authorize("lambda:DeleteFunction", lambdaARN(region, name)) {
			return
		}
		result, e := g.Lambdas.db.Exec("DELETE FROM aws_lambda_functions WHERE region=? AND name=?", region, name)
		if e != nil {
			lambdaError(w, e)
			return
		}
		n, e := result.RowsAffected()
		if e != nil {
			lambdaError(w, e)
			return
		}
		if n == 0 {
			lambdaError(w, ssmFailure{"ResourceNotFoundException", "Function not found."})
			return
		}
		lambdaJSON(w, 204, nil)
		return
	}
	if len(parts) == 2 && parts[1] == "configuration" && r.Method == "GET" {
		if !authorize("lambda:GetFunctionConfiguration", lambdaARN(region, name)) {
			return
		}
		f, e := g.Lambdas.get(region, name)
		if e != nil {
			lambdaError(w, e)
			return
		}
		lambdaJSON(w, 200, lambdaConfiguration(f))
		return
	}
	if len(parts) == 2 && parts[1] == "invocations" && r.Method == "POST" {
		if !authorize("lambda:InvokeFunction", lambdaARN(region, name)) {
			return
		}
		kind := r.Header.Get("X-Amz-Invocation-Type")
		if kind != "" && kind != "RequestResponse" && kind != "DryRun" || r.Header.Get("X-Amz-Log-Type") != "" && r.Header.Get("X-Amz-Log-Type") != "None" || r.Header.Get("X-Amz-Client-Context") != "" {
			lambdaError(w, ssmFailure{"NotImplementedException", "Only synchronous or DryRun invocation without log tail/client context is supported."})
			return
		}
		f, e := g.Lambdas.get(region, name)
		if e != nil {
			lambdaError(w, e)
			return
		}
		if e = iamExecutionRole(r.Context(), g.Lambdas.db, f.Role); e != nil {
			lambdaError(w, e)
			return
		}
		if kind == "DryRun" {
			lambdaJSON(w, 204, nil)
			return
		}
		event, e := io.ReadAll(http.MaxBytesReader(w, r.Body, pythonruntime.MaxOutputBytes))
		if e != nil {
			invalid("Invocation payload exceeds 256 KiB.")
			return
		}
		if len(event) == 0 {
			event = []byte("{}")
		}
		if !json.Valid(event) {
			invalid("Invocation payload must be JSON.")
			return
		}
		runner, e := g.Lambdas.runner(r.Context())
		if e != nil {
			lambdaError(w, e)
			return
		}
		result, e := runner.Run(r.Context(), pythonruntime.Invocation{Exchange: g.lambdaExchange(f, region), Modules: lambdaSDKFiles(), Code: f.Code, Handler: f.Handler, Event: event, Environment: f.Environment, Context: map[string]string{"function_name": f.Name, "function_version": "$LATEST", "invoked_function_arn": lambdaARN(region, name)}, Timeout: time.Duration(f.Timeout) * time.Second})
		if e != nil {
			result.FunctionError = true
			result.Payload, _ = json.Marshal(map[string]string{"errorType": "Runtime.SandboxError", "errorMessage": "Function execution stopped: time, memory, output, cancellation or sandbox limit."})
		}
		if result.FunctionError {
			w.Header().Set("X-Amz-Function-Error", "Unhandled")
		}
		w.Header().Set("X-Amz-Executed-Version", "$LATEST")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(result.Payload)
		return
	}
	lambdaError(w, ssmFailure{"NotImplementedException", "This Lambda operation is not implemented."})
}

// SDK service scope takes precedence over path shape: an S3 object may have a
// key identical to a Lambda API path. Unsigned console requests select explicitly.
func isLambda(r *http.Request) bool {
	if r.Header.Get("X-Simulator-Service") == "lambda" {
		return true
	}
	_, scope, ok := strings.Cut(r.Header.Get("Authorization"), "Credential=")
	parts := strings.Split(scope, "/")
	return ok && len(parts) > 3 && parts[3] == "lambda"
}
