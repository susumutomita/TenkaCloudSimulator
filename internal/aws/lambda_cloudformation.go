package aws

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	pythonruntime "github.com/susumutomita/TenkaCloudSimulator/internal/workload/python"
)

// Inline Python uses index.py as required by CloudFormation. It is executed by
// the same bounded interpreter as native ZIP functions, without any cloud SDK.
func compileStackFunction(region, stack, stackID, logical string, properties map[string]any) (LambdaFunction, error) {
	var f LambdaFunction
	fail := func(message string) (LambdaFunction, error) { return f, ssmFailure{"ValidationError", message} }
	for key := range properties {
		switch key {
		case "FunctionName", "Description", "Runtime", "Handler", "Timeout", "MemorySize", "Role", "Environment", "Tags", "Code", "PackageType":
		default:
			return fail("Unsupported Lambda property: " + key)
		}
	}
	f.Name = stringValue(properties["FunctionName"])
	if raw, exists := properties["FunctionName"]; exists {
		name, valid := raw.(string)
		if !valid || !lambdaName.MatchString(name) {
			return fail("Invalid Lambda FunctionName.")
		}
	} else {
		base := stack + "-" + logical
		if len(base) > 47 {
			base = base[:47]
		}
		sum := sha256.Sum256([]byte(stackID + "\x00" + logical))
		f.Name = base + "-" + hex.EncodeToString(sum[:8])
	}
	if !lambdaName.MatchString(f.Name) {
		return fail("Cannot generate a valid Lambda function name.")
	}
	f.Region = region
	f.Handler = stringValue(properties["Handler"])
	f.Role = stringValue(properties["Role"])
	if stringValue(properties["Runtime"]) != "python3.12" || !strings.HasPrefix(f.Handler, "index.") || !localRole.MatchString(f.Role) {
		return fail("Use Python 3.12, index.function and an existing local execution-role ARN.")
	}
	if raw, exists := properties["PackageType"]; exists && raw != "Zip" {
		return fail("Only Zip package type is supported.")
	}
	if raw, exists := properties["Description"]; exists {
		value, valid := raw.(string)
		if !valid || len(value) > 256 {
			return fail("Description must be a string of at most 256 bytes.")
		}
		f.Description = value
	}
	f.Timeout = 3
	if raw, exists := properties["Timeout"]; exists {
		timeout, err := strconv.Atoi(stringValue(raw))
		if err != nil || timeout < 1 || timeout > 30 {
			return fail("Timeout must be 1–30 seconds.")
		}
		f.Timeout = timeout
	}
	if raw, exists := properties["MemorySize"]; exists && stringValue(raw) != "128" {
		return fail("Only 128 MiB Lambda memory is supported.")
	}
	code, ok := properties["Code"].(map[string]any)
	if !ok || len(code) != 1 {
		return fail("Code requires only inline ZipFile Python source.")
	}
	source, ok := code["ZipFile"].(string)
	if !ok || len(source) == 0 || len(source) > pythonruntime.MaxCodeBytes {
		return fail("Inline Python source must be 1–256 KiB.")
	}
	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	entry, err := writer.Create("index.py")
	if err != nil {
		return f, err
	}
	if _, err = entry.Write([]byte(source)); err != nil {
		return f, err
	}
	if err = writer.Close(); err != nil {
		return f, err
	}
	f.Code = buffer.Bytes()
	if _, err = pythonruntime.ValidateCode(f.Code, f.Handler); err != nil {
		return fail(err.Error())
	}
	f.Environment = map[string]string{}
	if raw, exists := properties["Environment"]; exists {
		environment, ok := raw.(map[string]any)
		if !ok || len(environment) != 1 {
			return fail("Environment requires only Variables.")
		}
		variables, ok := environment["Variables"].(map[string]any)
		if !ok {
			return fail("Environment Variables must be an object.")
		}
		size := 0
		for key, raw := range variables {
			value, ok := raw.(string)
			if !ok || !regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]+$`).MatchString(key) || strings.ContainsRune(value, '\x00') {
				return fail("Invalid function environment key/value.")
			}
			size += len(key) + len(value)
			f.Environment[key] = value
		}
		if len(variables) > 64 || size > 4096 {
			return fail("Environment exceeds local limits.")
		}
	}
	f.Tags = map[string]string{}
	if raw, exists := properties["Tags"]; exists {
		tags, ok := raw.([]any)
		if !ok || len(tags) > 50 {
			return fail("Tags must be an array of at most 50 entries.")
		}
		for _, raw := range tags {
			tag, ok := raw.(map[string]any)
			if !ok || len(tag) != 2 {
				return fail("Tags require Key and Value.")
			}
			key, k := tag["Key"].(string)
			value, v := tag["Value"].(string)
			_, duplicate := f.Tags[key]
			if !k || !v || key == "" || len(key) > 128 || len(value) > 256 || duplicate {
				return fail("Invalid or duplicate function tag.")
			}
			f.Tags[key] = value
		}
	}
	var revision [16]byte
	if _, err = rand.Read(revision[:]); err != nil {
		return f, fmt.Errorf("function revision: %w", err)
	}
	f.Revision = hex.EncodeToString(revision[:])
	f.Modified = time.Now().UTC().Format("2006-01-02T15:04:05.000+0000")
	return f, nil
}
