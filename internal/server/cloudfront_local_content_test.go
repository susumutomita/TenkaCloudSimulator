package server

import (
	"encoding/xml"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func TestCloudFrontSavedContentUsesAuthenticatedDownloadProtections(t *testing.T) {
	store, err := core.New("")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err = store.Create("local-origin", "us-east-1"); err != nil {
		t.Fatal(err)
	}
	const content = "benign saved local text"
	object, err := store.Put("local-origin", "asset.txt", "text/html", []byte(content), "", "")
	if err != nil {
		t.Fatal(err)
	}
	front, err := aws.NewCloudFrontStore(store.Database())
	if err != nil {
		t.Fatal(err)
	}
	const token = "CLOUDFRONT_LOCAL_FIXTURE_OPERATOR_12345678901234567890"
	access, err := NewAccess(&aws.Gateway{CloudFront: front, Store: store, Identities: core.NewIdentities(store)}, token, false)
	if err != nil {
		t.Fatal(err)
	}
	body := `<DistributionConfig><CallerReference>download</CallerReference><Origins><Quantity>1</Quantity><Items><Origin><Id>saved</Id><DomainName>local-origin.s3.amazonaws.com</DomainName><S3OriginConfig><OriginAccessIdentity></OriginAccessIdentity></S3OriginConfig></Origin></Items></Origins><DefaultCacheBehavior><TargetOriginId>saved</TargetOriginId><ViewerProtocolPolicy>allow-all</ViewerProtocolPolicy><AllowedMethods><Quantity>2</Quantity><Items><Method>GET</Method><Method>HEAD</Method></Items><CachedMethods><Quantity>2</Quantity><Items><Method>GET</Method><Method>HEAD</Method></Items></CachedMethods></AllowedMethods><ForwardedValues><QueryString>false</QueryString><Cookies><Forward>none</Forward></Cookies></ForwardedValues><MinTTL>0</MinTTL><DefaultTTL>60</DefaultTTL><MaxTTL>86400</MaxTTL></DefaultCacheBehavior><Comment>local text</Comment><Enabled>true</Enabled></DistributionConfig>`
	r := httptest.NewRequest("POST", "/2020-05-31/distribution", strings.NewReader(body))
	r.Host = "127.0.0.1:12345"
	r.Header.Set("X-Simulator-Token", token)
	r.Header.Set("X-Simulator-Service", "cloudfront")
	created := httptest.NewRecorder()
	access.ServeHTTP(created, r)
	if created.Code != 201 {
		t.Fatal("authenticated distribution create", created.Code)
	}
	var value struct{ Id string }
	if err = xml.Unmarshal(created.Body.Bytes(), &value); err != nil || value.Id == "" {
		t.Fatal("missing distribution", err)
	}
	for _, method := range []string{"GET", "HEAD"} {
		path := "/_simulator/cloudfront/distributions/" + value.Id + "/objects/asset.txt"
		request := httptest.NewRequest(method, path, nil)
		request.Host = "127.0.0.1:12345"
		request.Header.Set("X-Simulator-Token", token)
		got := httptest.NewRecorder()
		access.ServeHTTP(got, request)
		if got.Code != 200 || got.Header().Get("ETag") != object.ETag || got.Header().Get("Content-Type") != "text/html" || got.Header().Get("Content-Disposition") != "attachment" || got.Header().Get("X-Content-Type-Options") != "nosniff" || got.Header().Get("Content-Security-Policy") != "sandbox; default-src 'none'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'" {
			t.Fatal("saved content lacks authenticated download protections", method, got.Code)
		}
		if method == "GET" && got.Body.String() != content || method == "HEAD" && got.Body.Len() != 0 {
			t.Fatal("download body changed", method)
		}
		denied := httptest.NewRecorder()
		unauthenticated := httptest.NewRequest(method, path, nil)
		unauthenticated.Host = "127.0.0.1:12345"
		access.ServeHTTP(denied, unauthenticated)
		if denied.Code != 401 {
			t.Fatal("missing local authority was accepted", method, denied.Code)
		}
	}
}
