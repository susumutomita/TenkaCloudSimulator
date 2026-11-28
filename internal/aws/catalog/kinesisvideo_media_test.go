package catalog

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestKinesisVideoRetainedMedia(t *testing.T) {
	h := newHarness(t, kinesisVideo)
	var clock atomic.Int64
	clock.Store(h.now.UnixMilli())
	h.engine.cfg.Now = func() time.Time { return time.UnixMilli(clock.Load()) }
	deny := func(action string, denied bool) {
		h.policy.mu.Lock()
		defer h.policy.mu.Unlock()
		h.policy.deny[action] = denied
	}
	server := httptest.NewServer(h.engine)
	t.Cleanup(server.Close)
	clip, err := os.ReadFile("../kinesisvideo/testdata/two-frames.mkv")
	if err != nil {
		t.Fatal(err)
	}
	call := func(path string, body []byte, headers map[string]string, want int) (http.Header, []byte) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, server.URL+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		sign(req, "kinesisvideo", "us-east-1")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != want {
			t.Fatalf("%s = %d, body %q, error %v", path, res.StatusCode, data, err)
		}
		return res.Header, data
	}
	makeStream := func(name string, hours int) string {
		t.Helper()
		created := h.rest("kinesisvideo", "POST", "/createStream", map[string]any{"StreamName": name, "DataRetentionInHours": hours})
		expect(t, created, 200, "")
		return str(created.body["StreamARN"])
	}
	get := func(name string, selector map[string]string, status int) []byte {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"StreamName": name, "StartSelector": selector})
		_, data := call("/getMedia", body, nil, status)
		return data
	}
	put := func(name, start string, input []byte, status int) []map[string]any {
		t.Helper()
		_, out := call("/putMedia", input, map[string]string{"X-Amzn-Stream-Name": name, "X-Amzn-Producer-Start-Timestamp": start, "X-Amzn-Fragment-Timecode-Type": "RELATIVE"}, status)
		if status != 200 {
			return nil
		}
		var acks []map[string]any
		for _, line := range bytes.Split(bytes.TrimSpace(out), []byte{'\n'}) {
			var item map[string]any
			if err := json.Unmarshal(line, &item); err != nil {
				t.Fatal(err)
			}
			acks = append(acks, item)
		}
		return acks
	}
	arn := makeStream("frames", 24)
	makeStream("other", 24)
	_, endpoint := call("/getDataEndpoint", []byte(`{"StreamName":"frames","APIName":"PUT_MEDIA"}`), map[string]string{"X-Forwarded-Host": "example.invalid"}, 200)
	var location struct{ DataEndpoint string }
	if json.Unmarshal(endpoint, &location) != nil || location.DataEndpoint != server.URL {
		t.Fatalf("unexpected returned endpoint %s", endpoint)
	}
	earliest := map[string]string{"StartSelectorType": "EARLIEST"}
	for _, bad := range []string{
		`{"StreamName":"other","StreamName":"frames","APIName":"GET_MEDIA"}`,
		`{"StreamName":"frames","APIName":"GET_MEDIA"}]`,
		`{"StreamName":"frames","APIName":[]}`,
		`{"StreamName":"frames","APIName":"GET_MEDIA","StreamARN":""}`,
	} {
		call("/getDataEndpoint", []byte(bad), nil, 400)
	}
	call("/getMedia", []byte(`{"StreamName":"frames","StartSelector":{"StartSelectorType":"CONTINUATION_TOKEN","StartSelectorType":"EARLIEST"}}`), nil, 400)
	if got := get("frames", earliest, 200); len(got) != 0 {
		t.Fatal("empty stream returned bytes")
	}
	events := put("frames", "1740000000.125", clip, 200)
	if len(events) != 3 || events[0]["EventType"] != "BUFFERING" || events[1]["EventType"] != "RECEIVED" || events[2]["EventType"] != "PERSISTED" || events[2]["FragmentNumber"] == "" {
		t.Fatal(events)
	}
	var cursor string
	if err := h.db.QueryRow("SELECT cursor FROM aws_kinesisvideo_fragments WHERE stream_arn=?", arn).Scan(&cursor); err != nil {
		t.Fatal(err)
	}
	clock.Add(time.Second.Milliseconds())
	second := put("frames", "2025-02-19T21:20:01.125Z", clip, 200)
	if second[2]["FragmentNumber"] == events[2]["FragmentNumber"] {
		t.Fatal("reused fragment number")
	}
	media := get("frames", earliest, 200)
	if bytes.Count(media, []byte("AWS_KINESISVIDEO_FRAGMENT_NUMBER")) != 2 || !bytes.Contains(media, []byte(cursor)) || !bytes.Contains(media, []byte("1740000000.125")) {
		t.Fatal("missing real fragments and metadata")
	}
	resume := map[string]string{"StartSelectorType": "CONTINUATION_TOKEN", "ContinuationToken": cursor}
	remaining := get("frames", resume, 200)
	if bytes.Count(remaining, []byte("AWS_KINESISVIDEO_FRAGMENT_NUMBER")) != 1 || bytes.Contains(remaining, []byte(cursor)) {
		t.Fatal("incorrect continuation")
	}
	get("other", resume, 400)
	deny("kinesisvideo:PutMedia "+arn, true)
	put("frames", "1740000002.125", clip, 401)
	deny("kinesisvideo:PutMedia "+arn, false)
	put("frames", "1740000002.125", clip[:20], 400)
	if got := get("frames", earliest, 200); !bytes.Equal(got, media) {
		t.Fatal("rejected upload changed retained media")
	}
	deny("kinesisvideo:GetMedia "+arn, true)
	get("frames", earliest, 401)
	deny("kinesisvideo:GetMedia "+arn, false)
	if got := get("other", earliest, 200); len(got) != 0 {
		t.Fatal("sibling stream was changed")
	}
	noRetention := makeStream("transient", 0)
	transient := put("transient", "1740000000", clip, 200)
	if len(transient) != 2 || transient[1]["EventType"] != "RECEIVED" || transient[1]["FragmentNumber"] != nil || len(get("transient", earliest, 200)) != 0 {
		t.Fatal("unexpected retained result for", noRetention)
	}
	version := str(h.rest("kinesisvideo", "POST", "/describeStream", map[string]any{"StreamARN": arn}).body["StreamInfo"].(map[string]any)["Version"])
	expect(t, h.rest("kinesisvideo", "POST", "/updateDataRetention", map[string]any{"StreamARN": arn, "CurrentVersion": version, "Operation": "DECREASE_DATA_RETENTION", "DataRetentionChangeInHours": 24}), 200, "")
	if len(get("frames", earliest, 200)) != 0 {
		t.Fatal("retention decrease preserved expired bytes")
	}
	get("frames", resume, 400)
	expect(t, h.rest("kinesisvideo", "POST", "/deleteStream", map[string]any{"StreamARN": arn}), 200, "")
	newARN := makeStream("frames", 24)
	if newARN == arn || !strings.HasSuffix(newARN, "/"+strconv.FormatInt(clock.Load()+1, 10)) && !strings.HasSuffix(newARN, "/"+strconv.FormatInt(clock.Load(), 10)) {
		t.Fatal("unexpected replacement ARN", newARN)
	}
	get("frames", resume, 400)
	old, _ := json.Marshal(map[string]any{"StreamARN": arn, "StartSelector": earliest})
	call("/getMedia", old, nil, 404)
}
