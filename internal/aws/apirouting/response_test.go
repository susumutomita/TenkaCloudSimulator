package apirouting

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestProxyResponseRequiresDocumentedShapeAndBounds(t *testing.T) {
	for _, input := range []string{`{"statusCode":200,"multiValueHeaders":{"bad name":[]}}`, `null`, `{}`, `{"statusCode":199}`, `{"statusCode":600}`, `{"statusCode":200,"body":{}}`, `{"statusCode":200,"isBase64Encoded":true}`, `{"statusCode":200} {}`, `{"statusCode":200,"headers":{"X-Test":"injected\r\nSet-Cookie: stolen"}}`, `{"statusCode":200,"headers":{"bad name":"x"}}`, `{"statusCode":200,"other":true}`, strings.Repeat(" ", MaxProxyEventBytes+1)} {
		if _, err := ParseProxyResponse([]byte(input)); err == nil {
			t.Fatal("invalid response accepted", input[:min(len(input), 80)])
		}
	}
	response, err := ParseProxyResponse([]byte(`{"statusCode":422,"body":"application failure","headers":{"X-App-Values":"one"},"multiValueHeaders":{"x-app-values":["two","three"]}}`))
	if err != nil || response.StatusCode != 422 || response.Body != "application failure" {
		t.Fatal(response, err)
	}
	headers := DownloadHeaders(response)
	if got := headers.Values("X-App-Values"); len(got) != 2 || got[0] != "two" || got[1] != "three" {
		t.Fatal(headers)
	}
}
func TestGuestHTMLAndHeadersCannotControlOperatorOrigin(t *testing.T) {
	response, err := ParseProxyResponse([]byte(`{"statusCode":302,"body":"<script>fetch('/_simulator/v1/access')</script>","headers":{"Content-Type":"text/html","Content-Disposition":"inline","Location":"/_simulator/console/","Set-Cookie":"tcsim_operator=stolen; Path=/","Content-Security-Policy":"default-src *","Access-Control-Allow-Origin":"*","X-Simulator-Token":"stolen","X-App-Result":"dummy"}}`))
	if err != nil {
		t.Fatal(err)
	}
	headers := DownloadHeaders(response)
	for _, name := range []string{"Location", "Set-Cookie", "Access-Control-Allow-Origin", "X-Simulator-Token"} {
		if headers.Get(name) != "" {
			t.Fatal("guest controls protected response header", name)
		}
	}
	if headers.Get("Content-Type") != "application/octet-stream" || !strings.HasPrefix(headers.Get("Content-Disposition"), "attachment;") || !strings.Contains(headers.Get("Content-Security-Policy"), "sandbox;") || headers.Get("X-App-Result") != "dummy" {
		t.Fatal(headers)
	}
}

func TestProxyResponseBoundsExpandedRepeatedHeaders(t *testing.T) {
	values := make([]string, 3000)
	response := ProxyResponse{StatusCode: 200, MultiValueHeaders: map[string][]string{strings.Repeat("A", 100): values}}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) >= MaxProxyEventBytes {
		t.Fatal("test must exercise expansion rather than raw payload size")
	}
	if _, err = ParseProxyResponse(encoded); err == nil {
		t.Fatal("repeated header-name expansion exceeded the response limit")
	}
	for _, input := range []string{`{"statusCode":200,"headers":{"":"value"}}`, `{"statusCode":200,"multiValueHeaders":{"X-App-Test":["invalid\r\nvalue"]}}`, `{"statusCode":200,"headers":{"X-App-Test":"invalid\u0000value"}}`} {
		if _, err = ParseProxyResponse([]byte(input)); err == nil {
			t.Fatal("invalid response header accepted", input)
		}
	}
}
