package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	sqlitedriver "github.com/ncruces/go-sqlite3/driver"
	"github.com/susumutomita/TenkaCloudSimulator/internal/aws"
)

type sesDomainClient struct {
	t                        *testing.T
	h                        http.Handler
	token, region, principal string
}

func (c sesDomainClient) call(service, method, path, body string, status int) []byte {
	c.t.Helper()
	r := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
	r.Header.Set("X-Simulator-Token", c.token)
	r.Header.Set("X-Simulator-Service", service)
	r.Header.Set("X-Simulator-Region", c.region)
	if c.principal != "" {
		r.Header.Set("X-Simulator-Principal", c.principal)
	}
	if service == "ses" {
		r.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	c.h.ServeHTTP(w, r)
	if w.Code != status {
		c.t.Fatalf("%s %s %s expected %d got %d: %s", service, method, path, status, w.Code, w.Body.String())
	}
	return w.Body.Bytes()
}

func (c sesDomainClient) zone(domain, ref string) string {
	c.t.Helper()
	data := c.call("route53", "POST", "/2013-04-01/hostedzone", "<CreateHostedZoneRequest><Name>"+domain+"</Name><CallerReference>"+ref+"</CallerReference></CreateHostedZoneRequest>", 201)
	var result struct {
		ID string `xml:"HostedZone>Id"`
	}
	if err := xml.Unmarshal(data, &result); err != nil || result.ID == "" {
		c.t.Fatal("missing real hosted zone", err)
	}
	return strings.TrimPrefix(result.ID, "/hostedzone/")
}

func (c sesDomainClient) record(zone, action, domain, token, destination string, status int) {
	c.t.Helper()
	body := fmt.Sprintf("<ChangeResourceRecordSetsRequest><ChangeBatch><Changes><Change><Action>%s</Action><ResourceRecordSet><Name>%s._domainkey.%s.</Name><Type>CNAME</Type><TTL>60</TTL><ResourceRecords><ResourceRecord><Value>%s.</Value></ResourceRecord></ResourceRecords></ResourceRecordSet></Change></Changes></ChangeBatch></ChangeResourceRecordSetsRequest>", action, token, domain, destination)
	c.call("route53", "POST", "/2013-04-01/hostedzone/"+zone+"/rrset", body, status)
}

func (c sesDomainClient) identity(method, domain string, status int) []string {
	c.t.Helper()
	const path = "/v2/email/identities"
	target, body := path+"/"+domain, ""
	if method == "POST" {
		target = path
		body = fmt.Sprintf(`{"EmailIdentity":%q}`, domain)
	}
	data := c.call("ses", method, target, body, status)
	if status != 200 || method == "DELETE" {
		return nil
	}
	var r struct {
		IdentityType             string
		VerifiedForSendingStatus bool
		VerificationStatus       string
		DkimAttributes           struct {
			Status string
			Tokens []string
		}
	}
	if err := json.Unmarshal(data, &r); err != nil {
		c.t.Fatal(err)
	}
	want := r.DkimAttributes.Status
	if r.IdentityType != "DOMAIN" || len(r.DkimAttributes.Tokens) != 3 || (want != "PENDING" && want != "SUCCESS") || r.VerifiedForSendingStatus != (want == "SUCCESS") || method == "POST" && want != "PENDING" || method == "GET" && r.VerificationStatus != want {
		c.t.Fatalf("inconsistent verified identity %#v", r)
	}
	if r.DkimAttributes.Tokens[0] == r.DkimAttributes.Tokens[1] || r.DkimAttributes.Tokens[0] == r.DkimAttributes.Tokens[2] || r.DkimAttributes.Tokens[1] == r.DkimAttributes.Tokens[2] {
		c.t.Fatal("duplicate verification tokens")
	}
	return append([]string{want}, r.DkimAttributes.Tokens...)
}

func openSESDomainClient(t *testing.T, filename, token string) (sesDomainClient, func() error) {
	t.Helper()
	h, close, err := New(filename)
	if err != nil {
		t.Fatal(err)
	}
	access, err := NewAccess(h, strings.Repeat(token, 64), false)
	if err != nil {
		t.Fatal(err)
	}
	return sesDomainClient{t: t, h: access, token: strings.Repeat(token, 64), region: "us-east-1"}, close
}

func TestSESLocalDomainRequiresActualRoute53Proof(t *testing.T) {
	const domain = "owned.test"
	const path = "/v2/email/identities"
	var closeOwner func() error

	filename := filepath.Join(t.TempDir(), "owner.db")
	owner, closeOwner := openSESDomainClient(t, filename, "a")
	defer func() {
		if err := closeOwner(); err != nil {
			t.Fatal(err)
		}
	}()
	peer, closePeer := openSESDomainClient(t, filepath.Join(t.TempDir(), "peer.db"), "b")
	defer closePeer()
	owner.identity("POST", "outside.example", 400)
	owner.identity("POST", domain, 400)
	zone := owner.zone(domain, "first")
	created := owner.identity("POST", domain, 200)
	owner.identity("POST", domain, 400)
	tokens := created[1:]
	reader := owner
	reader.principal = "unauthorized"
	reader.identity("GET", domain, 403)
	peer.identity("GET", domain, 404)
	other := owner
	other.region = "us-west-2"
	other.identity("GET", domain, 404)
	read := func(expected string) {
		t.Helper()
		value := owner.identity("GET", domain, 200)
		if value[0] != expected || !slices.Equal(value[1:], tokens) {
			t.Fatal("domain verification or tokens differed", expected, value[0])
		}
	}
	read("PENDING")
	reader.record(zone, "CREATE", domain, tokens[0], tokens[0]+".dkim.amazonses.com", 403)
	for _, token := range tokens[:2] {
		owner.record(zone, "CREATE", domain, token, token+".dkim.amazonses.com", 200)
	}
	owner.record(zone, "CREATE", domain, tokens[2], "incorrect.test", 200)
	read("PENDING")
	owner.record(zone, "UPSERT", domain, tokens[2], tokens[2]+".dkim.amazonses.com", 200)
	read("SUCCESS")
	list := owner.call("ses", "GET", path+"?PageSize=1", "", 200)
	if !strings.Contains(string(list), `"VerificationStatus":"SUCCESS"`) || !strings.Contains(string(list), domain) {
		t.Fatal("list omitted native proof", string(list))
	}
	if err := closeOwner(); err != nil {
		t.Fatal(err)
	}
	owner, closeOwner = openSESDomainClient(t, filename, "a")
	read("SUCCESS")
	z2 := owner.zone(domain, "different duplicate")
	read("PENDING")
	owner.call("route53", "DELETE", "/2013-04-01/hostedzone/"+z2, "", 200)
	read("SUCCESS")
	for _, token := range tokens {
		owner.record(zone, "DELETE", domain, token, token+".dkim.amazonses.com", 200)
	}
	read("PENDING")
	owner.call("route53", "DELETE", "/2013-04-01/hostedzone/"+zone, "", 200)
	z3 := owner.zone(domain, "new physical zone")
	for _, token := range tokens {
		owner.record(z3, "CREATE", domain, token, token+".dkim.amazonses.com", 200)
	}
	read("PENDING")
	owner.identity("DELETE", domain, 200)
	owner.identity("GET", domain, 404)
	fresh := owner.identity("POST", domain, 200)
	for _, token := range fresh[1:] {
		if slices.Contains(tokens, token) {
			t.Fatal("new lifetime adopted retired token")
		}
	}
	owner.identity("DELETE", domain, 200)
	for _, token := range tokens {
		owner.record(z3, "DELETE", domain, token, token+".dkim.amazonses.com", 200)
	}
	owner.call("route53", "DELETE", "/2013-04-01/hostedzone/"+z3, "", 200)
}

func TestSESLocalDomainScopeAndPagination(t *testing.T) {
	const path = "/v2/email/identities"
	owner, closeOwner := openSESDomainClient(t, filepath.Join(t.TempDir(), "owner.db"), "a")
	defer closeOwner()
	peer, closePeer := openSESDomainClient(t, filepath.Join(t.TempDir(), "peer.db"), "b")
	defer closePeer()
	const first = "first.test"
	const second = "second.test"
	firstZone := owner.zone(first, "first")
	secondZone := owner.zone(second, "second")
	principal := owner
	arn := "arn:aws:ses:us-east-1:000000000000:identity/" + first
	policy := func(actions, resource string) {
		t.Helper()
		owner.call("", "PUT", "/_simulator/v1/iam/principals/identity-editor/policy", fmt.Sprintf(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":%s,"Resource":%q}]}`, actions, resource), 200)
	}
	policy(`["ses:CreateEmailIdentity","ses:GetEmailIdentity","ses:DeleteEmailIdentity","ses:ListEmailIdentities"]`, arn)
	var session struct{ AccessToken string }
	data := owner.call("", "POST", "/_simulator/v1/access", `{"Principal":"identity-editor"}`, 200)
	if err := json.Unmarshal(data, &session); err != nil || session.AccessToken == "" {
		t.Fatal("participant has no issued local token", err)
	}
	principal.token = session.AccessToken
	created := principal.identity("POST", first, 200)
	principal.identity("GET", first, 200)
	identityTags := fmt.Sprintf(`{"ResourceArn":%q,"Tags":[{"Key":"team","Value":"owner"}]}`, arn)
	principal.call("ses", "POST", "/v2/email/tags", identityTags, 403)
	owner.call("ses", "POST", "/v2/email/tags", identityTags, 400)
	owner.call("ses", "GET", "/v2/email/tags?ResourceArn="+arn, "", 400)
	principal.identity("GET", second, 403)
	principal.identity("POST", second, 403)
	principal.call("ses", "GET", path, "", 403)
	principal.record(firstZone, "CREATE", first, created[1], created[1]+".dkim.amazonses.com", 403)
	owner.identity("POST", second, 200)
	principal.identity("DELETE", second, 403)
	policy(`"ses:ListEmailIdentities"`, "*")
	parse := func(data []byte, domain string) string {
		t.Helper()
		var response struct {
			EmailIdentities []struct{ IdentityName, VerificationStatus string }
			NextToken       string
		}
		if err := json.Unmarshal(data, &response); err != nil || len(response.EmailIdentities) != 1 || response.EmailIdentities[0].IdentityName != domain || response.EmailIdentities[0].VerificationStatus != "PENDING" {
			t.Fatalf("invalid saved identity page for %s: %#v %v", domain, response, err)
		}
		return response.NextToken
	}
	marker := parse(principal.call("ses", "GET", path+"?PageSize=1", "", 200), first)
	if len(marker) != 32 {
		t.Fatal("missing continuation")
	}
	next := path + "?PageSize=1&NextToken=" + marker
	if parse(principal.call("ses", "GET", next, "", 200), second) != "" {
		t.Fatal("unexpected additional page")
	}
	other := owner
	other.region = "us-west-2"
	other.identity("POST", first, 200)
	other.call("ses", "GET", next, "", 400)
	peer.call("ses", "GET", next, "", 400)
	principal.identity("GET", first, 403)
	for _, q := range []string{"?NextToken=bogus", "?PageSize=0", "?PageSize=101", "?PageSize=invalid"} {
		owner.call("ses", "GET", path+q, "", 400)
	}
	for _, body := range []string{`{"EmailIdentity":"unused.test","Tags":[]}`, `{"EmailIdentity":"unused.test","ConfigurationSetName":"preserve"}`, `{"EmailIdentity":"user@first.test"}`} {
		owner.call("ses", "POST", path, body, 400)
	}
	owner.identity("DELETE", first, 200)
	owner.call("ses", "GET", next, "", 400)
	other.identity("GET", first, 200)
	other.identity("DELETE", first, 200)
	owner.identity("DELETE", second, 200)
	owner.call("route53", "DELETE", "/2013-04-01/hostedzone/"+firstZone, "", 200)
	owner.call("route53", "DELETE", "/2013-04-01/hostedzone/"+secondZone, "", 200)
}

func TestSESDomainMutationInterruptedAfterPhysicalRow(t *testing.T) {
	for _, method := range []string{"POST", "DELETE"} {
		for _, reason := range []error{context.Canceled, context.DeadlineExceeded} {
			t.Run(method+"/"+reason.Error(), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "owner.db")
				var db *sql.DB
				h, closeDB, err := NewWithProviders(path, ProviderConfig{CloudStorage: DefaultCloudStorageConfig(), configureInstances: func(d *sql.DB, _ *aws.StackStore) error { db = d; return nil }})
				if err != nil {
					t.Fatal(err)
				}
				access, err := NewAccess(h, strings.Repeat("a", 64), false)
				if err != nil {
					t.Fatal(err)
				}
				client := sesDomainClient{t: t, h: access, token: strings.Repeat("a", 64), region: "us-east-1"}
				const name = "interrupt.test"
				zone := client.zone(name, "ownership")
				siblingZone := client.zone("sibling.test", "sibling")
				sibling := client.identity("POST", "sibling.test", 200)
				event, alias, target, body := "INSERT", "NEW", "/v2/email/identities", `{"EmailIdentity":"interrupt.test"}`
				var previous []string
				if method == "DELETE" {
					previous = client.identity("POST", name, 200)
					event, alias, target, body = "DELETE", "OLD", target+"/"+name, ""
				}
				before := func() string {
					t.Helper()
					var frame string
					err := db.QueryRow(`SELECT json_array(
						(SELECT json_group_array(json_array(seq,region,domain,zone_id,marker,first_token,second_token,third_token) ORDER BY seq) FROM aws_ses_domain_identities),
						(SELECT seq FROM sqlite_sequence WHERE name='aws_ses_domain_identities'),
						(SELECT json_group_array(json_array(id,name) ORDER BY id) FROM aws_route53_zones),
						(SELECT json_group_array(json_array(zone,name,type,document) ORDER BY zone,name,type) FROM aws_route53_records))`).Scan(&frame)
					if err != nil {
						t.Fatal(err)
					}
					return frame
				}
				initial := before()
				var ctx context.Context
				var cancel context.CancelFunc
				observed := false
				conn, err := db.Conn(t.Context())
				if err != nil {
					t.Fatal(err)
				}
				err = conn.Raw(func(raw any) error {
					return raw.(sqlitedriver.Conn).Raw().CreateFunction("staged_identity", 6, 0, func(result sqlite3.Context, args ...sqlite3.Value) {
						a, b, c := args[3].Text(), args[4].Text(), args[5].Text()
						observed = args[0].Text() == name && args[1].Text() == zone && args[2].Int64() > 0 && len(a) == 32 && len(b) == 32 && len(c) == 32 && a != b && a != c && b != c
						if method == "DELETE" {
							observed = observed && slices.Equal([]string{a, b, c}, previous[1:])
						}
						if reason == context.Canceled {
							cancel()
						}
						<-ctx.Done()
						result.ResultNull()
					})
				})
				if err == nil {
					source := strings.ReplaceAll(`CREATE TEMP TRIGGER current_ses_write AFTER EVENT ON aws_ses_domain_identities
						WHEN ROW.domain='interrupt.test' BEGIN SELECT staged_identity(ROW.domain,ROW.zone_id,ROW.seq,ROW.first_token,ROW.second_token,ROW.third_token); END`, "EVENT", event)
					_, err = conn.ExecContext(t.Context(), strings.ReplaceAll(source, "ROW", alias))
				}
				conn.Close()
				if err != nil {
					t.Fatal(err)
				}
				if reason == context.Canceled {
					ctx, cancel = context.WithCancel(t.Context())
				} else {
					ctx, cancel = context.WithTimeout(t.Context(), 500*time.Millisecond)
				}
				defer cancel()
				req := httptest.NewRequestWithContext(ctx, method, "http://127.0.0.1"+target, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("X-Simulator-Token", client.token)
				req.Header.Set("X-Simulator-Region", client.region)
				req.Header.Set("X-Simulator-Service", "ses")
				w := httptest.NewRecorder()
				access.ServeHTTP(w, req)
				if !observed || !errors.Is(ctx.Err(), reason) || w.Code < 400 || strings.Contains(w.Body.String(), "DkimAttributes") {
					t.Fatalf("interrupted staged native write was acknowledged or absent: observed=%t http=%d err=%v", observed, w.Code, ctx.Err())
				}
				if err = closeDB(); err != nil {
					t.Fatal(err)
				}
				reopened, closeDB, err := NewWithProviders(path, ProviderConfig{CloudStorage: DefaultCloudStorageConfig(), configureInstances: func(d *sql.DB, _ *aws.StackStore) error { db = d; return nil }})
				if err != nil {
					t.Fatal(err)
				}
				defer closeDB()
				client.h, err = NewAccess(reopened, client.token, false)
				if err != nil {
					t.Fatal(err)
				}
				if before() != initial || !slices.Equal(sibling, client.identity("GET", "sibling.test", 200)) {
					t.Fatal("interruption changed identity, sibling or DNS state after reopen")
				}
				client.identity(method, name, 200)
				if method == "POST" {
					client.identity("GET", name, 200)
					client.identity("DELETE", name, 200)
				}
				client.identity("GET", name, 404)
				client.identity("DELETE", "sibling.test", 200)
				client.call("route53", "DELETE", "/2013-04-01/hostedzone/"+zone, "", 200)
				client.call("route53", "DELETE", "/2013-04-01/hostedzone/"+siblingZone, "", 200)
			})
		}
	}
}
