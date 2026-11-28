package catalog

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"
)

func TestKinesisVideoPublishedMediaOperations(t *testing.T) {
	h := newHarness(t, kinesisVideo)
	descriptor := h.engine.Describe()[0]
	if descriptor.Resources[0].Ops["read"].Name != "DescribeStream" || !slices.Contains(descriptor.Operations, "GetDataEndpoint") {
		t.Fatal("stream detail and data endpoint discovery must both remain accessible")
	}
	paths := h.engine.OpenAPI()["kinesisvideo"]["paths"].(map[string]any)
	type parameter struct {
		Name     string
		Required bool
		Example  string
	}
	type operation struct {
		Parameters  []parameter
		RequestBody struct {
			Content map[string]struct{ Example json.RawMessage }
		}
		Responses map[string]struct{ Content map[string]any }
	}
	server := httptest.NewServer(h.engine)
	t.Cleanup(server.Close)
	sample := "workshop-video"
	expect(t, h.rest("kinesisvideo", "POST", "/createStream", map[string]any{"StreamName": sample, "DataRetentionInHours": 1}), 200, "")
	send := func(path, inputType, outputType string, upload []byte) []byte {
		t.Helper()
		raw, err := json.Marshal(paths[path].(map[string]any)["post"])
		var op operation
		if err != nil || json.Unmarshal(raw, &op) != nil {
			t.Fatalf("invalid published operation %s", path)
		}
		input, exists := op.RequestBody.Content[inputType]
		if !exists || op.Responses["200"].Content[outputType] == nil {
			t.Fatalf("incompatible wire media for %s", path)
		}
		if upload == nil {
			upload = input.Example
		}
		req, err := http.NewRequest(http.MethodPost, server.URL+path, bytes.NewReader(upload))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", inputType)
		for _, p := range op.Parameters {
			switch p.Name {
			case "X-Simulator-Service":
				if !p.Required || p.Example != "kinesisvideo" {
					t.Fatal("missing first-party service routing")
				}
				req.Header.Set(p.Name, p.Example)
			case "x-amzn-stream-name":
				req.Header.Set(p.Name, sample)
			case "x-amzn-fragment-timecode-type":
				req.Header.Set(p.Name, "RELATIVE")
			case "x-amzn-producer-start-timestamp":
				req.Header.Set(p.Name, "1740000000.125")
			}
		}
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		reply, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != 200 || res.Header.Get("Content-Type") != outputType {
			t.Fatalf("%s returned %s, %s, %v", path, res.Status, reply, err)
		}
		return reply
	}
	var endpoint struct{ DataEndpoint string }
	if err := json.Unmarshal(send("/getDataEndpoint", "application/json", "application/json", nil), &endpoint); err != nil || endpoint.DataEndpoint != server.URL {
		t.Fatalf("unexpected discovered origin %v %v", endpoint, err)
	}
	clip, err := os.ReadFile("../kinesisvideo/testdata/two-frames.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(send("/putMedia", "application/octet-stream", "application/json", clip), []byte("PERSISTED")) {
		t.Fatal("published upload did not persist")
	}
	if !bytes.Contains(send("/getMedia", "application/json", "video/x-matroska", nil), []byte("AWS_KINESISVIDEO_FRAGMENT_NUMBER")) {
		t.Fatal("published reader did not return the stored media")
	}
}
