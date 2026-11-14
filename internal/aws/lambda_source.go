package aws

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
	pythonruntime "github.com/susumutomita/TenkaCloudSimulator/internal/workload/python"
)

// ServeLambdaSource is a simulator-only source editing API, not an AWS Lambda
// operation. The server must wrap it in its normal local token/session access
// boundary. Source reads require GetFunction; edits require both GetFunction
// and UpdateFunctionCode because they read and return the saved source package.
func (g *Gateway) ServeLambdaSource(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	invalid := func(message string) { lambdaError(w, ssmFailure{"InvalidParameterValueException", message}) }
	if g.Lambdas == nil {
		lambdaError(w, ssmFailure{"NotImplementedException", "Lambda state is not configured."})
		return
	}
	// This route cannot validate AWS signatures. Do not silently treat signed
	// requests as the default local administrator or accept native AWS options.
	if r.Header.Get("Authorization") != "" || r.URL.RawQuery != "" || r.Header.Get("X-Amz-Target") != "" {
		invalid("Use the local session source API without AWS signing or query options.")
		return
	}
	if service := r.Header.Get("X-Simulator-Service"); service != "" && service != "lambda" {
		invalid("Use the Lambda service scope.")
		return
	}
	region, err := lambdaRegion(r)
	if err != nil {
		lambdaError(w, err)
		return
	}
	const prefix = "/_simulator/v1/lambda/"
	if !strings.HasPrefix(r.URL.Path, prefix) || !strings.HasSuffix(r.URL.Path, "/source") {
		invalid("Unsupported local source path.")
		return
	}
	name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, prefix), "/source")
	if !lambdaName.MatchString(name) {
		invalid("Use an unqualified local function name.")
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPut {
		w.Header().Set("Allow", "GET, PUT")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	principal := r.Header.Get("X-Simulator-Principal")
	if principal == "" {
		principal = core.Administrator
	}
	policy, sessionPolicy := SDKPolicyFromContext(r.Context())
	if !sessionPolicy && principal != core.Administrator {
		if g.Identities == nil {
			lambdaError(w, ssmFailure{"AccessDeniedException", "Identity policy is unavailable."})
			return
		}
		policy, _, err = g.Identities.Policy(principal)
		if err != nil {
			lambdaError(w, err)
			return
		}
	}
	_, parameterBound := r.Context().Value(consoleParameterBoundaryKey{}).(consoleParameterBoundary)
	actions := []string{"lambda:GetFunction"}
	if r.Method == http.MethodPut {
		actions = append(actions, "lambda:UpdateFunctionCode")
	}
	for _, action := range actions {
		decision := "allowed"
		if parameterBound {
			decision = "implicitDeny"
		} else if sessionPolicy || principal != core.Administrator {
			decision = core.Evaluate(policy, action, lambdaARN(region, name))
		}
		w.Header().Set("X-Simulator-IAM-Decision", decision)
		if decision != "allowed" {
			lambdaError(w, ssmFailure{"AccessDeniedException", "IAM evaluation: " + decision})
			return
		}
	}
	f, err := g.Lambdas.get(region, name)
	if err != nil {
		lambdaError(w, err)
		return
	}
	path, source, err := lambdaSavedSource(f)
	if err != nil {
		invalid("The saved package cannot be edited: " + err.Error())
		return
	}
	if r.Method == http.MethodPut {
		input, e := lambdaSourceInput(w, r)
		if e != nil {
			invalid(e.Error())
			return
		}
		if input["RevisionId"] != f.Revision {
			lambdaError(w, ssmFailure{"PreconditionFailedException", "Function revision changed."})
			return
		}
		code, e := lambdaReplaceSource(f, input["Source"])
		if e != nil {
			invalid(e.Error())
			return
		}
		var revision [16]byte
		if _, e = rand.Read(revision[:]); e != nil {
			lambdaError(w, e)
			return
		}
		f.Code, f.Revision = code, hex.EncodeToString(revision[:])
		f.Modified = time.Now().UTC().Format("2006-01-02T15:04:05.000+0000")
		if e = g.Lambdas.save(&f, false, input["RevisionId"]); e != nil {
			lambdaError(w, e)
			return
		}
		source = input["Source"]
	}
	result := lambdaConfiguration(f)
	result["Source"], result["Path"] = source, path
	lambdaJSON(w, http.StatusOK, result)
}

func lambdaSourceInput(w http.ResponseWriter, r *http.Request) (map[string]string, error) {
	// JSON escaping can require six bytes per source byte; the decoded source
	// and final ZIP are independently bounded below.
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 6*pythonruntime.MaxExpandedBytes+1024))
	if err != nil || !utf8.Valid(data) {
		return nil, errors.New("Use a bounded UTF-8 source request.")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, errors.New("Use a source request object.")
	}
	fields := map[string]string{}
	for d.More() {
		key, e := d.Token()
		if e != nil {
			return nil, e
		}
		name, ok := key.(string)
		if !ok || (name != "Source" && name != "RevisionId") {
			return nil, errors.New("Only Source and RevisionId are supported.")
		}
		if _, exists := fields[name]; exists {
			return nil, errors.New("Duplicate source fields are unsupported.")
		}
		value, e := d.Token()
		if e != nil {
			return nil, e
		}
		text, ok := value.(string)
		if !ok {
			return nil, errors.New("Source and RevisionId must be strings.")
		}
		fields[name] = text
	}
	if _, err = d.Token(); err != nil {
		return nil, err
	}
	if d.Decode(new(any)) != io.EOF || len(fields) != 2 || fields["RevisionId"] == "" || len(fields["RevisionId"]) > 128 {
		return nil, errors.New("Supply Source and the current RevisionId in one JSON object.")
	}
	return fields, nil
}

func lambdaSavedSource(f LambdaFunction) (string, string, error) {
	z, err := pythonruntime.ValidateCode(f.Code, f.Handler)
	if err != nil {
		return "", "", err
	}
	parts := strings.Split(f.Handler, ".")
	path := strings.Join(parts[:len(parts)-1], "/") + ".py"
	for _, file := range z.File {
		if file.Name != path {
			continue
		}
		reader, e := file.Open()
		if e != nil {
			return "", "", e
		}
		data, e := io.ReadAll(io.LimitReader(reader, pythonruntime.MaxExpandedBytes+1))
		_ = reader.Close()
		if e != nil {
			return "", "", e
		}
		if len(data) > pythonruntime.MaxExpandedBytes || !lambdaSourceUTF8(data) {
			return "", "", errors.New("Handler source must be bounded UTF-8 with a UTF-8 Python encoding declaration; use ZIP upload for other encodings.")
		}
		return path, string(data), nil
	}
	return "", "", errors.New("Handler source is missing.")
}

type lambdaSourceBuffer struct{ bytes.Buffer }

func (b *lambdaSourceBuffer) Write(p []byte) (int, error) {
	if len(p) > pythonruntime.MaxCodeBytes-b.Len() {
		return 0, errors.New("Updated ZIP exceeds 256 KiB.")
	}
	return b.Buffer.Write(p)
}

func lambdaReplaceSource(f LambdaFunction, source string) ([]byte, error) {
	if len(source) > pythonruntime.MaxExpandedBytes || !lambdaSourceUTF8([]byte(source)) {
		return nil, errors.New("Handler source must be bounded UTF-8 with a UTF-8 Python encoding declaration; use ZIP upload for other encodings.")
	}
	path, _, err := lambdaSavedSource(f)
	if err != nil {
		return nil, err
	}
	z, err := zip.NewReader(bytes.NewReader(f.Code), int64(len(f.Code)))
	if err != nil {
		return nil, err
	}
	var buffer lambdaSourceBuffer
	writer := zip.NewWriter(&buffer)
	if err = writer.SetComment(z.Comment); err != nil {
		return nil, err
	}
	for _, file := range z.File {
		if file.Name != path {
			// Copy retains each untouched entry's compressed bytes and metadata. No
			// extraction to the host or guessed reconstruction of imported modules.
			if err = writer.Copy(file); err != nil {
				return nil, err
			}
			continue
		}
		header := file.FileHeader
		target, e := writer.CreateHeader(&header)
		if e != nil {
			return nil, e
		}
		if _, e = io.WriteString(target, source); e != nil {
			return nil, e
		}
	}
	if err = writer.Close(); err != nil {
		return nil, err
	}
	result := buffer.Bytes()
	if _, err = pythonruntime.ValidateCode(result, f.Handler); err != nil {
		return nil, err
	}
	return result, nil
}

// PEP 263 declarations affect Python's interpretation even when every stored
// byte is valid UTF-8. Never transcode or strip a BOM/cookie silently. Inspect
// only an effective cookie: line two is eligible when line one is blank or a
// comment. The accepted codec aliases are deliberately limited to UTF-8.
var lambdaCodingCookie = regexp.MustCompile(`^[ \t\f]*#.*?coding[:=][ \t]*([-_.a-zA-Z0-9]+)`)
var lambdaCookiePreamble = regexp.MustCompile(`^[ \t\f]*(#|$)`)

func lambdaSourceUTF8(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	bom := bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if bom {
		data = data[3:]
	}
	// Python universal newlines include CR-only files.
	text := strings.ReplaceAll(strings.ReplaceAll(string(data), "\r\n", "\n"), "\r", "\n")
	lines := strings.SplitN(text, "\n", 3)
	for i := 0; i < len(lines) && i < 2; i++ {
		cookie := lambdaCodingCookie.FindStringSubmatch(lines[i])
		if cookie != nil {
			encoding := strings.ToLower(strings.ReplaceAll(cookie[1], "_", "-"))
			// CPython normalizes utf-8-* cookie spellings to utf-8 before
			// checking BOM compatibility. Other UTF-8 aliases are not BOM-compatible.
			canonical := encoding == "utf-8" || strings.HasPrefix(encoding, "utf-8-")
			if bom {
				return canonical
			}
			return canonical || encoding == "utf8" || encoding == "u8" || encoding == "utf" || encoding == "cp65001"
		}
		if i == 0 && !lambdaCookiePreamble.MatchString(lines[0]) {
			break
		}
	}
	return true
}
