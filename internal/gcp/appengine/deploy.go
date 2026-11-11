package appengine

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/url"
	"sort"
	"strings"

	pythonruntime "github.com/susumutomita/TenkaCloudSimulator/internal/workload/python"
)

// adapter is the trusted WSGI glue added to every runtime bundle. Deployed
// files may not use its name.
const adapterName = "_tcsim_appengine.py"

// Deployment bounds follow the embedded runtime: at most 63 Python sources
// besides the adapter, 1 MiB expanded and 256 KiB compressed.
const (
	maxFiles         = 63
	maxExpandedBytes = pythonruntime.MaxExpandedBytes - 16<<10
)

// sourceURL parses the official Cloud Storage form
// http(s)://storage.googleapis.com/<bucket>/<object>.
func sourceURL(raw string) (string, string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host != "storage.googleapis.com" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", "", invalid("sourceUrl must be https://storage.googleapis.com/<bucket>/<object>: " + raw)
	}
	bucket, object, ok := strings.Cut(strings.TrimPrefix(u.EscapedPath(), "/"), "/")
	if !ok || bucket == "" || object == "" {
		return "", "", invalid("sourceUrl must name a bucket and an object: " + raw)
	}
	if bucket, err = url.PathUnescape(bucket); err != nil {
		return "", "", invalid("sourceUrl has an invalid bucket: " + raw)
	}
	if object, err = url.PathUnescape(object); err != nil {
		return "", "", invalid("sourceUrl has an invalid object name: " + raw)
	}
	return bucket, object, nil
}

func (e *Engine) fetch(ctx context.Context, raw string, limit int64) ([]byte, error) {
	bucket, object, err := sourceURL(raw)
	if err != nil {
		return nil, err
	}
	data, err := e.cfg.Source(ctx, bucket, object, limit)
	switch {
	case errors.Is(err, ErrSourceNotFound):
		return nil, invalid("Deployment source " + raw + " is not an object in a Cloud Storage bucket of project " + e.cfg.Project + ".")
	case errors.Is(err, ErrSourceTooLarge):
		return nil, invalid("Deployment source " + raw + " exceeds the local deployment size limit.")
	case err != nil:
		return nil, err
	}
	return data, nil
}

func validSourcePath(name string) bool {
	first, _, _ := strings.Cut(name, "/")
	return fs.ValidPath(name) && name != "." && !strings.ContainsAny(name, "\\\x00") && strings.HasSuffix(name, ".py") && !strings.HasPrefix(first, "_tcsim")
}

// deploy copies the deployment's Cloud Storage objects into a normalized
// ZIP of Python sources, as App Engine copies files at deployment time; a
// later change to the objects does not change the version. It returns the
// code and the deployment as the version stores and returns it.
func (e *Engine) deploy(ctx context.Context, raw json.RawMessage) ([]byte, json.RawMessage, error) {
	var deployment map[string]json.RawMessage
	if err := json.Unmarshal(raw, &deployment); err != nil || deployment == nil {
		return nil, nil, invalid("deployment must be a Deployment object.")
	}
	for name := range deployment {
		switch name {
		case "zip", "files":
		case "container", "cloudBuildOptions":
			return nil, nil, invalid("deployment." + name + " is not supported; deploy Python sources as a zip or files manifest.")
		default:
			return nil, nil, invalid("Unknown field deployment." + name + ".")
		}
	}
	files := map[string][]byte{}
	var normalized any
	switch {
	case deployment["zip"] != nil && deployment["files"] != nil:
		return nil, nil, invalid("deployment takes either zip or files, not both.")
	case deployment["zip"] != nil:
		var info map[string]json.RawMessage
		if err := json.Unmarshal(deployment["zip"], &info); err != nil || info == nil {
			return nil, nil, invalid("deployment.zip must be a ZipInfo object.")
		}
		source, count := "", int64(-1)
		for name, value := range info {
			switch name {
			case "sourceUrl":
				if json.Unmarshal(value, &source) != nil {
					return nil, nil, invalid("deployment.zip.sourceUrl must be a string.")
				}
			case "filesCount":
				if json.Unmarshal(value, &count) != nil || count < 0 {
					return nil, nil, invalid("deployment.zip.filesCount must be a non-negative integer.")
				}
			default:
				return nil, nil, invalid("Unknown field deployment.zip." + name + ".")
			}
		}
		data, err := e.fetch(ctx, source, pythonruntime.MaxCodeBytes)
		if err != nil {
			return nil, nil, err
		}
		if files, err = unpack(data); err != nil {
			return nil, nil, err
		}
		if count >= 0 && count < int64(len(files)) {
			return nil, nil, invalid("deployment.zip.filesCount must be at least the number of files in the zip.")
		}
		zipInfo := map[string]any{"sourceUrl": source}
		if count >= 0 {
			zipInfo["filesCount"] = count
		}
		normalized = map[string]any{"zip": zipInfo}
	case deployment["files"] != nil:
		var manifest map[string]map[string]json.RawMessage
		if err := json.Unmarshal(deployment["files"], &manifest); err != nil || len(manifest) == 0 {
			return nil, nil, invalid("deployment.files must map file paths to FileInfo objects.")
		}
		if len(manifest) > maxFiles {
			return nil, nil, invalid("A local App Engine deployment has at most 63 files.")
		}
		names := make([]string, 0, len(manifest))
		for name := range manifest {
			names = append(names, name)
		}
		sort.Strings(names)
		out, budget := map[string]any{}, int64(maxExpandedBytes)
		for _, name := range names {
			if !validSourcePath(name) {
				return nil, nil, invalid("deployment.files may contain only Python sources (.py) with safe relative paths: " + name)
			}
			entry := map[string]string{}
			for field, value := range manifest[name] {
				var text string
				if (field != "sourceUrl" && field != "sha1Sum" && field != "mimeType") || json.Unmarshal(value, &text) != nil {
					return nil, nil, invalid("deployment.files." + name + " has an unknown or non-string field " + field + ".")
				}
				entry[field] = text
			}
			data, err := e.fetch(ctx, entry["sourceUrl"], budget)
			if err != nil {
				return nil, nil, err
			}
			if sum := entry["sha1Sum"]; sum != "" {
				actual := sha1.Sum(data)
				if !strings.EqualFold(sum, hex.EncodeToString(actual[:])) {
					return nil, nil, invalid("deployment.files." + name + ".sha1Sum does not match the Cloud Storage object.")
				}
			}
			budget -= int64(len(data))
			files[name] = data
			info := map[string]any{"sourceUrl": entry["sourceUrl"]}
			for _, field := range []string{"sha1Sum", "mimeType"} {
				if entry[field] != "" {
					info[field] = entry[field]
				}
			}
			out[name] = info
		}
		normalized = map[string]any{"files": out}
	default:
		return nil, nil, invalid("deployment needs a zip or a files manifest.")
	}
	if files["main.py"] == nil {
		return nil, nil, invalid("The deployment must contain main.py with a WSGI callable named app (the default entrypoint main:app).")
	}
	code, err := pack(files)
	if err != nil {
		return nil, nil, err
	}
	if _, err := bundle(code); err != nil {
		return nil, nil, err
	}
	document, _ := json.Marshal(normalized)
	return code, document, nil
}

// unpack reads a deployment ZIP of Python sources and directories.
func unpack(data []byte) (map[string][]byte, error) {
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, invalid("The deployment zip could not be read.")
	}
	files, expanded := map[string][]byte{}, int64(0)
	for _, file := range z.File {
		if file.FileInfo().IsDir() {
			continue
		}
		if !validSourcePath(file.Name) || !file.Mode().IsRegular() || files[file.Name] != nil {
			return nil, invalid("The deployment zip may contain only Python sources (.py) with safe unique relative paths: " + file.Name)
		}
		if len(files) >= maxFiles {
			return nil, invalid("A local App Engine deployment has at most 63 files.")
		}
		r, err := file.Open()
		if err != nil {
			return nil, invalid("The deployment zip could not be read.")
		}
		content, err := io.ReadAll(io.LimitReader(r, maxExpandedBytes-expanded+1))
		_ = r.Close()
		if err != nil {
			return nil, invalid("The deployment zip could not be read.")
		}
		expanded += int64(len(content))
		if expanded > maxExpandedBytes {
			return nil, invalid("The deployment exceeds the local limit of 1 MiB of Python sources.")
		}
		files[file.Name] = content
	}
	return files, nil
}

// pack writes files in name order, so equal sources give equal bytes.
func pack(files map[string][]byte) ([]byte, error) {
	names := make([]string, 0, len(files))
	size := 0
	for name, data := range files {
		names = append(names, name)
		size += len(data)
	}
	if size > maxExpandedBytes {
		return nil, invalid("The deployment exceeds the local limit of 1 MiB of Python sources.")
	}
	sort.Strings(names)
	var buffer bytes.Buffer
	w := zip.NewWriter(&buffer)
	for _, name := range names {
		f, err := w.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			return nil, err
		}
		if _, err = f.Write(files[name]); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// bundle adds the trusted adapter to stored code and validates the result
// against the runtime's own ZIP rules.
func bundle(code []byte) ([]byte, error) {
	z, err := zip.NewReader(bytes.NewReader(code), int64(len(code)))
	if err != nil {
		return nil, errors.New("stored App Engine code is corrupt")
	}
	var buffer bytes.Buffer
	w := zip.NewWriter(&buffer)
	for _, file := range z.File {
		if err := w.Copy(file); err != nil {
			return nil, err
		}
	}
	f, err := w.CreateHeader(&zip.FileHeader{Name: adapterName, Method: zip.Deflate})
	if err != nil {
		return nil, err
	}
	if _, err = f.Write(adapterSource); err != nil {
		return nil, err
	}
	if err = w.Close(); err != nil {
		return nil, err
	}
	if _, err := pythonruntime.ValidateCode(buffer.Bytes(), "_tcsim_appengine.handle"); err != nil {
		return nil, invalid("The deployment exceeds the local runtime limits (64 files, 1 MiB expanded, 256 KiB compressed).")
	}
	return buffer.Bytes(), nil
}

func countFiles(code []byte) int {
	z, err := zip.NewReader(bytes.NewReader(code), int64(len(code)))
	if err != nil {
		return 0
	}
	return len(z.File)
}

func digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
