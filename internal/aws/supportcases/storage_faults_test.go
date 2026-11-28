package supportcases

import (
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ncruces/go-sqlite3"
	"github.com/ncruces/go-sqlite3/driver"
)

// The store uses a single connection per team. Install the fault on that
// connection, then let the service run its ordinary database/sql transaction.
func supportAuthorizer(t *testing.T, db *sql.DB, fn func(sqlite3.AuthorizerActionCode, string, string, string, string) sqlite3.AuthorizerReturnCode) {
	t.Helper()
	conn, err := db.Conn(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.Raw(func(raw any) error { return raw.(driver.Conn).Raw().SetAuthorizer(fn) }); err != nil {
		t.Fatal(err)
	}
}

func supportDurableFrame(t *testing.T, db *sql.DB) []string {
	t.Helper()
	frame := supportRows(t, db)
	var sequence string
	err := db.QueryRow(`SELECT coalesce(group_concat(name||'='||seq,','),'') FROM
		(SELECT name,seq FROM sqlite_sequence WHERE name LIKE 'aws_support_%' ORDER BY name)`).Scan(&sequence)
	if err != nil {
		t.Fatal(err)
	}
	return append(frame, sequence)
}

func TestSupportOperationsFailAtomicallyAtDatabaseAccess(t *testing.T) {
	for _, operation := range []string{"CreateCase", "AddAttachmentsToSet", "AppendToSet", "ResolveCase", "AddCommunicationToCase", "DescribeAttachment", "DescribeCases", "DescribeCommunications"} {
		t.Run(operation, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "team.db")
			host, s := openTeam(t, path)
			files := []Attachment{{Name: "one.bin", Data: []byte{0, 255}}, {Name: "two.bin", Data: []byte{255, 0, 3}}}
			set := call(t, s, "AddAttachmentsToSet", Input{Attachments: files})["attachmentSetId"].(string)
			id := call(t, s, "CreateCase", Input{Subject: "target", Body: "first", SetID: set})["caseId"].(string)
			call(t, s, "CreateCase", Input{Subject: "sibling", Body: "keep", SetID: set})
			peerHost, peer := openTeam(t, filepath.Join(t.TempDir(), "peer.db"))
			peerSet := call(t, peer, "AddAttachmentsToSet", Input{Attachments: files})["attachmentSetId"].(string)
			call(t, peer, "CreateCase", Input{Subject: "peer", Body: "keep all bytes", SetID: peerSet})
			peerFrame := supportDurableFrame(t, peerHost.Database())
			communication := call(t, s, "DescribeCommunications", Input{CaseID: id})["communications"].([]map[string]any)[0]
			file := communication["attachmentSet"].([]map[string]string)[0]["attachmentId"]
			input := map[string]Input{
				"CreateCase":             {Subject: "new", Body: "first", SetID: set},
				"AddAttachmentsToSet":    {Attachments: files},
				"AppendToSet":            {SetID: set, Attachments: files[:1]},
				"ResolveCase":            {CaseID: id},
				"AddCommunicationToCase": {CaseID: id, Body: "new reply", SetID: set},
				"DescribeAttachment":     {AttachmentID: file},
				"DescribeCases":          {CaseIDs: []string{id}, DisplayID: "1"},
				"DescribeCommunications": {CaseID: id},
			}[operation]
			op := operation
			if op == "AppendToSet" {
				op = "AddAttachmentsToSet"
			}
			before := supportDurableFrame(t, host.Database())

			// Every reachable access in the request is denied in turn, including
			// reads after attachment insertion and reads of nested result pages.
			for cutoff := 1; ; cutoff++ {
				if cutoff > 512 {
					t.Fatal("could not reach end of operation")
				}
				var seen int
				var refused bool
				supportAuthorizer(t, host.Database(), func(action sqlite3.AuthorizerActionCode, table, column, _, _ string) sqlite3.AuthorizerReturnCode {
					if strings.HasPrefix(table, "aws_support_") || action == sqlite3.AUTH_FUNCTION && column == "total_changes" ||
						action == sqlite3.AUTH_TRANSACTION && table == "COMMIT" {
						seen++
						if seen == cutoff {
							refused = true
							return sqlite3.AUTH_DENY
						}
					}
					return sqlite3.AUTH_OK
				})
				reply, err := s.Run(t.Context(), op, "local-author", input)
				supportAuthorizer(t, host.Database(), nil)
				if !refused {
					if cutoff == 1 || err != nil || reply == nil {
						t.Fatalf("clean retry at %d: %v", cutoff, err)
					}
					break
				}
				if err == nil || reply != nil {
					t.Fatalf("acknowledged denied database access %d: %v, %v", cutoff, reply, err)
				}
				if !slices.Equal(before, supportDurableFrame(t, host.Database())) {
					t.Fatalf("refusal %d changed saved state or allocated identifiers", cutoff)
				}
				if err := host.Close(); err != nil {
					t.Fatal(err)
				}
				host, s = openTeam(t, path)
				if !slices.Equal(before, supportDurableFrame(t, host.Database())) {
					t.Fatalf("refusal %d changed reopened state", cutoff)
				}
			}
			if !slices.Equal(peerFrame, supportDurableFrame(t, peerHost.Database())) {
				t.Fatal("separate team changed")
			}
		})
	}
}

func TestSupportInputAndPaginationBoundaries(t *testing.T) {
	host, s := openTeam(t, "")
	id := call(t, s, "CreateCase", Input{Subject: "range", Body: "kept"})["caseId"].(string)
	frame := supportDurableFrame(t, host.Database())
	for _, test := range []struct {
		op string
		in Input
	}{
		{"CreateCase", Input{Body: "required subject"}}, {"CreateCase", Input{Subject: strings.Repeat("界", 257), Body: "long"}},
		{"CreateCase", Input{Subject: "request", Body: "initial", IssueType: "absent"}},
		{"CreateCase", Input{Subject: "request", Body: "initial", Language: "absent"}},
		{"AddCommunicationToCase", Input{Body: "missing case"}}, {"AddCommunicationToCase", Input{CaseID: id, Body: strings.Repeat("界", 8001)}},
		{"AddAttachmentsToSet", Input{}}, {"AddAttachmentsToSet", Input{Attachments: []Attachment{{Name: "missing data"}}}},
		{"AddAttachmentsToSet", Input{Attachments: []Attachment{{Name: strings.Repeat("界", 256), Data: []byte{0}}}}},
		{"DescribeCases", Input{MaxResults: 9}}, {"DescribeCases", Input{MaxResults: 101}}, {"DescribeCases", Input{CaseIDs: make([]string, 101)}},
		{"DescribeCases", Input{DisplayID: "01"}}, {"DescribeCases", Input{NextToken: "short"}},
		{"DescribeCommunications", Input{CaseID: id, AfterTime: "bad"}}, {"DescribeCommunications", Input{CaseID: id, BeforeTime: "bad"}},
	} {
		wantFault(t, s, test.op, test.in, "ValidationException")
	}
	wantFault(t, s, "AddCommunicationToCase", Input{CaseID: id, Body: "kept", SetID: "absent"}, "AttachmentSetIdNotFound")
	wantFault(t, s, "DescribeAttachment", Input{AttachmentID: "absent"}, "AttachmentIdNotFound")
	if out, err := s.Run(t.Context(), "DescribeCases", "", Input{}); out != nil || err == nil || err.Error() == "" {
		t.Fatal("missing actor yielded response")
	}
	wantFault(t, s, "Unsupported", Input{}, "UnknownOperationException")
	if !slices.Equal(frame, supportDurableFrame(t, host.Database())) {
		t.Fatal("invalid input mutated saved data")
	}
	omit := false
	for i := 0; i < 12; i++ {
		call(t, s, "CreateCase", Input{Subject: "page", Body: "boundary"})
	}
	first := call(t, s, "DescribeCases", Input{MaxResults: 10, IncludeCommunications: &omit})
	token := first["nextToken"].(string)
	second := call(t, s, "DescribeCases", Input{MaxResults: 10, IncludeCommunications: &omit, NextToken: token})
	if len(first["cases"].([]map[string]any)) != 10 || len(second["cases"].([]map[string]any)) != 3 || second["nextToken"] != nil {
		t.Fatal("pagination did not partition all saved cases")
	}
	wantFault(t, s, "DescribeCases", Input{NextToken: token, IncludeResolved: true}, "ValidationException")
	if got := call(t, s, "DescribeCases", Input{DisplayID: "1"})["cases"].([]map[string]any); len(got) != 1 || got[0]["caseId"] != id {
		t.Fatal("display ID did not identify original case")
	}
	if _, err := New(nil); err == nil {
		t.Fatal("initialized without a database")
	}
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := New(host.Database()); err == nil {
		t.Fatal("initialized a closed database")
	}
	var fault Fault
	if out, err := s.Run(t.Context(), "DescribeCases", "local-author", Input{}); out != nil || err == nil || errors.As(err, &fault) {
		t.Fatal("storage failure reported as a client fault")
	}
}
