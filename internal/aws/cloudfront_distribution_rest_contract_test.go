package aws

import (
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func cfRESTError(t *testing.T, w *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	var response struct {
		XMLName xml.Name
		Code    string `xml:"Error>Code"`
		Message string `xml:"Error>Message"`
	}
	if err := xml.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if w.Code != status || response.XMLName != (xml.Name{Space: cloudFrontNS, Local: "ErrorResponse"}) || response.Code != code || response.Message == "" {
		t.Fatalf("wrong native error: status=%d response=%+v", w.Code, response)
	}
	if w.Header().Get("ETag") != "" || w.Header().Get("Location") != "" {
		t.Fatal("refused request exposed success metadata")
	}
}

func TestCloudFrontRESTContractMalformedXMLPreservesState(t *testing.T) {
	h := cfDistHarnessFor(t, "")
	h.seed("contract-origin", "saved")
	h.create("existing", "contract-origin")
	valid := cfDistConfig("candidate", "contract-origin", true, 60)
	cases := map[string]string{
		"truncated":           valid[:len(valid)-1],
		"namespace":           strings.Replace(valid, cloudFrontNS, "urn:unsupported", 1),
		"wrong-root":          strings.ReplaceAll(valid, "DistributionConfig", "UnsupportedConfig"),
		"second-root":         valid + valid,
		"root-attribute":      strings.Replace(valid, "<DistributionConfig ", `<DistributionConfig unsupported="yes" `, 1),
		"nested-attribute":    strings.Replace(valid, "<Comment>", `<Comment unsupported="yes">`, 1),
		"duplicate-scalar":    strings.Replace(valid, "</Comment>", "</Comment><Comment>again</Comment>", 1),
		"missing-required":    strings.Replace(valid, "<Comment>local only</Comment>", "", 1),
		"unknown-child":       strings.Replace(valid, "</Comment>", "</Comment><Unsupported/>", 1),
		"text-before-child":   strings.Replace(valid, "<Origins>", "<Origins>mixed", 1),
		"text-outside-root":   "outside" + valid,
		"nested-processing":   strings.Replace(valid, "<Comment>", "<?unexpected value?><Comment>", 1),
		"trailing-processing": valid + "<?xml version=\"1.0\"?>",
		"directive":           "<!DOCTYPE DistributionConfig>" + valid,
		"invalid-bool":        strings.Replace(valid, "<Enabled>true", "<Enabled>perhaps", 1),
		"invalid-integer":     strings.Replace(valid, "<DefaultTTL>60", "<DefaultTTL>NaN", 1),
	}
	before := cfInterruptFrame(t, h.store.Database())
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			cfRESTError(t, h.call("POST", cfDistributionPath, body, "", "", 400), 400, "InvalidArgument")
			if !reflect.DeepEqual(before, cfInterruptFrame(t, h.store.Database())) {
				t.Fatal("invalid XML changed an existing distribution, receipt, cache or origin")
			}
		})
	}
	// The standard XML declaration and comments are not unsupported fields.
	accepted := h.call("POST", cfDistributionPath, `<?xml version="1.0"?><!--saved-->`+valid, "", "", 201)
	if accepted.Header().Get("Location") == "" || accepted.Header().Get("ETag") == "" {
		t.Fatal("valid XML did not produce actual native creation metadata")
	}
}

func TestCloudFrontRESTContractUnsupportedConfigurationAndInvalidation(t *testing.T) {
	h := cfDistHarnessFor(t, "")
	h.seed("contract-origin", "saved")
	d, _ := h.create("existing", "contract-origin")
	valid := cfDistConfig("candidate", "contract-origin", true, 60)
	changes := [][2]string{
		{"<CallerReference>candidate", "<CallerReference> padded "},
		{"<CallerReference>candidate", "<CallerReference>" + strings.Repeat("x", 129)},
		{"<Comment>local only", "<Comment>" + strings.Repeat("界", 43)},
		{"<Quantity>1</Quantity>", "<Quantity>2</Quantity>"},
		{"contract-origin.s3.amazonaws.com", "external.example"},
		{"contract-origin.s3.amazonaws.com", "ab.s3.amazonaws.com"},
		{"<Id>saved</Id>", "<Id></Id>"},
		{"<OriginAccessIdentity></OriginAccessIdentity>", "<OriginAccessIdentity>unsupported</OriginAccessIdentity>"},
		{"<TargetOriginId>saved", "<TargetOriginId>other"},
		{"<ViewerProtocolPolicy>allow-all", "<ViewerProtocolPolicy>https-only"},
		{"<Method>HEAD</Method>", "<Method>POST</Method>"},
		{"<QueryString>false", "<QueryString>true"},
		{"<Forward>none", "<Forward>all"},
		{"<MinTTL>0", "<MinTTL>-1"},
		{"<MinTTL>0", "<MinTTL>61"},
		{"<DefaultTTL>60", "<DefaultTTL>86401"},
		{"<MaxTTL>86400", "<MaxTTL>86401"},
	}
	before := cfInterruptFrame(t, h.store.Database())
	for i, change := range changes {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			body := strings.Replace(valid, change[0], change[1], 1)
			if body == valid {
				t.Fatal("test input did not change")
			}
			cfRESTError(t, h.call("POST", cfDistributionPath, body, "", "", 400), 400, "InvalidArgument")
		})
	}
	route := cfDistributionPath + "/" + d.Id + "/invalidation"
	for _, path := range []string{"asset.txt", "/a//b", "/a/../b", "/a/./b", "/a/%62", "/a*", "/" + strings.Repeat("x", 1025)} {
		cfRESTError(t, h.call("POST", route, cfInvalidate("invalid", path), "", "", 400), 400, "InvalidArgument")
	}
	for _, body := range []string{
		strings.Replace(cfInvalidate("invalid", "/asset.txt"), "<Quantity>1", "<Quantity>2", 1),
		strings.Replace(cfInvalidate("invalid", "/asset.txt"), "<Quantity>1", "<Quantity>101", 1),
		strings.Replace(strings.Replace(cfInvalidate("invalid", "/asset.txt"), "</Items>", "<Path>/asset.txt</Path></Items>", 1), "<Quantity>1", "<Quantity>2", 1),
		strings.Replace(cfInvalidate("invalid", "/asset.txt"), "<Quantity>1", "<Quantity>bad", 1),
		cfInvalidate(" padded ", "/asset.txt"),
	} {
		cfRESTError(t, h.call("POST", route, body, "", "", 400), 400, "InvalidArgument")
	}
	if !reflect.DeepEqual(before, cfInterruptFrame(t, h.store.Database())) {
		t.Fatal("invalid configuration or invalidation changed native state")
	}
	missing := "I" + strings.Repeat("a", 24)
	cfRESTError(t, h.call("GET", route+"/"+missing, "", "", "", 404), 404, "NoSuchInvalidation")
}

type cfRESTBrokenBody struct{}

func (cfRESTBrokenBody) Read([]byte) (int, error) { return 0, errors.New("owned unreadable body") }
func (cfRESTBrokenBody) Close() error             { return nil }

func TestCloudFrontRESTContractRoutesBodiesAndAuthority(t *testing.T) {
	h := cfDistHarnessFor(t, "")
	h.seed("contract-origin", "saved")
	d, _ := h.create("existing", "contract-origin")
	route := cfDistributionPath + "/" + d.Id
	before := cfInterruptFrame(t, h.store.Database())
	for _, test := range []struct{ method, path, body string }{
		{"PATCH", cfDistributionPath, ""}, {"GET", cfDistributionPath + "/bad", ""},
		{"POST", route, ""}, {"DELETE", route + "/config", ""}, {"GET", route + "/unknown", ""},
		{"GET", route + "?unsupported=true", ""}, {"GET", route, "not empty"},
		{"GET", cfDistributionPath + "?Marker=%zz", ""},
		{"GET", cfDistributionPath + "?Unknown=1", ""},
		{"GET", cfDistributionPath + "?Marker=bad", ""},
		{"GET", cfDistributionPath + "?MaxItems=abc", ""},
		{"GET", cfDistributionPath + "?MaxItems=51", ""},
		{"POST", cfDistributionPath, strings.Repeat("x", 32769)},
	} {
		cfRESTError(t, h.call(test.method, test.path, test.body, "", "", 400), 400, "InvalidArgument")
	}
	for _, mutate := range []func(*http.Request){
		func(r *http.Request) { r.URL.RawPath = r.URL.Path },
		func(r *http.Request) { r.Header.Set("X-Amz-Target", "unsupported") },
		func(r *http.Request) {
			r.Header.Add("X-Simulator-Principal", "first")
			r.Header.Add("X-Simulator-Principal", "second")
		},
		func(r *http.Request) { r.Body = cfRESTBrokenBody{} },
	} {
		r := httptest.NewRequest("GET", route, nil)
		mutate(r)
		w := httptest.NewRecorder()
		h.gateway.CloudFront.serveDistribution(h.gateway, w, r)
		cfRESTError(t, w, 400, "InvalidArgument")
	}
	// A missing authority is an internal failure, never default administrator.
	missingAuthority := httptest.NewRecorder()
	h.gateway.CloudFront.serveDistribution(&Gateway{}, missingAuthority, httptest.NewRequest("GET", route, nil))
	cfRESTError(t, missingAuthority, 500, "InternalError")
	policy, err := core.ParsePolicy([]byte(fmt.Sprintf(`{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"cloudfront:GetDistribution","Resource":%q}}`, d.ARN)))
	if err != nil {
		t.Fatal(err)
	}
	if err = h.gateway.Identities.Set("contract-reader", policy); err != nil {
		t.Fatal(err)
	}
	h.call("GET", route, "", "contract-reader", "", 200)
	other, _ := h.create("sibling", "contract-origin")
	cfRESTError(t, h.call("GET", cfDistributionPath+"/"+other.Id, "", "contract-reader", "", 403), 403, "AccessDenied")
	cfRESTError(t, h.call("GET", cfDistributionPath+"/D"+strings.Repeat("f", 24), "", "contract-reader", "", 403), 403, "AccessDenied")
	cfRESTError(t, h.call("GET", cfDistributionPath, "", "contract-reader", "", 403), 403, "AccessDenied")
	// Only the intentional sibling creation changes the previously saved frame.
	after := cfInterruptFrame(t, h.store.Database())
	if len(after) != len(before)+1 {
		t.Fatal("refused REST requests changed native state")
	}
	for _, row := range before {
		found := false
		for _, current := range after {
			found = found || row == current
		}
		if !found {
			t.Fatal("refusal changed an earlier row")
		}
	}
	request := httptest.NewRequest("GET", cfDistributionPath, nil)
	request.Header.Set("X-Simulator-Service", "s3")
	if isCFDistribution(request) {
		t.Fatal("cross-service route adopted")
	}
	request.Header.Del("X-Simulator-Service")
	request.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential=TCSIM/20260101/us-east-1/cloudfront/aws4_request, Signature=synthetic")
	if !isCFDistribution(request) {
		t.Fatal("native CloudFront signing scope not selected")
	}
	request.URL.Path = "/unrelated"
	if isCFDistribution(request) {
		t.Fatal("unrelated native path adopted")
	}
	var absent *CloudFrontStore
	w := httptest.NewRecorder()
	absent.serveDistribution(h.gateway, w, request)
	cfRESTError(t, w, 500, "InternalError")
}
