package supportcases

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/ncruces/go-sqlite3"
	sqlitedriver "github.com/ncruces/go-sqlite3/driver"
)

// Snapshot the owned case and all dependent tables, including the staged files
// that must remain available for a subsequent retry.
func supportRows(t *testing.T, db *sql.DB) []string {
	t.Helper()
	var frame []string
	for _, query := range []string{
		"SELECT json_array('case',display_id,case_id,subject,created_us,status,language,issue_type) FROM aws_support_cases ORDER BY display_id",
		"SELECT json_array('message',sequence,case_id,created_us,author,body) FROM aws_support_communications ORDER BY sequence",
		"SELECT json_array('set',set_id,expires_us) FROM aws_support_attachment_sets ORDER BY set_id",
		"SELECT json_array('staged',set_id,position,name,hex(data)) FROM aws_support_staged_files ORDER BY set_id,position",
		"SELECT json_array('attachment',attachment_id,sequence,position,name,hex(data)) FROM aws_support_attachments ORDER BY attachment_id",
	} {
		rows, err := db.QueryContext(t.Context(), query)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var row string
			if err := rows.Scan(&row); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			frame = append(frame, row)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		if err := rows.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return frame
}

func TestInterruptedCommunicationAndAttachmentsRollBack(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "owner.sqlite")
			host, owner := openTeam(t, path)
			peerHost, peer := openTeam(t, filepath.Join(t.TempDir(), "peer.sqlite"))
			files := []Attachment{{Name: "first.bin", Data: []byte{0, 255}}, {Name: "second.bin", Data: []byte{2, 201}}}
			setID := call(t, owner, "AddAttachmentsToSet", Input{Attachments: files})["attachmentSetId"].(string)
			id := call(t, owner, "CreateCase", Input{Subject: "Target", Body: "Original"})["caseId"].(string)
			sibling := call(t, owner, "CreateCase", Input{Subject: "Sibling", Body: "Retained", SetID: setID})["caseId"].(string)
			peerSet := call(t, peer, "AddAttachmentsToSet", Input{Attachments: files[:1]})["attachmentSetId"].(string)
			call(t, peer, "CreateCase", Input{Subject: "Separate team", Body: "Retained", SetID: peerSet})
			before := supportRows(t, host.Database())
			peerBefore := supportRows(t, peerHost.Database())

			var requestContext context.Context
			var stop context.CancelFunc
			var observed []string
			conn, err := host.Database().Conn(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			err = conn.Raw(func(raw any) error {
				return raw.(sqlitedriver.Conn).Raw().CreateFunction("observe_staged_support_reply", 5, 0,
					func(result sqlite3.Context, args ...sqlite3.Value) {
						for _, arg := range args {
							observed = append(observed, arg.Text())
						}
						if cause == context.Canceled {
							stop()
						}
						<-requestContext.Done()
						result.ResultNull()
					})
			})
			if err != nil {
				conn.Close()
				t.Fatal(err)
			}
			// The second INSERT has completed. Read the actual case, body and bytes
			// from this transaction while database/sql still owns the connection.
			_, err = conn.ExecContext(t.Context(), `CREATE TEMP TRIGGER interrupt_support_reply
				AFTER INSERT ON aws_support_attachments WHEN NEW.position=1 BEGIN
				SELECT observe_staged_support_reply(
				    (SELECT case_id FROM aws_support_communications WHERE sequence=NEW.sequence),
				    (SELECT body FROM aws_support_communications WHERE sequence=NEW.sequence),
				    (SELECT hex(data) FROM aws_support_attachments WHERE sequence=NEW.sequence AND position=0),
				    (SELECT hex(data) FROM aws_support_attachments WHERE sequence=NEW.sequence AND position=1),
				    NEW.attachment_id);
				END`)
			closeErr := conn.Close()
			if err != nil || closeErr != nil {
				t.Fatal(err, closeErr)
			}

			if cause == context.Canceled {
				requestContext, stop = context.WithCancel(t.Context())
			} else {
				requestContext, stop = context.WithTimeout(t.Context(), 2*time.Second)
			}
			defer stop()
			input := Input{CaseID: id, Body: "Interrupted follow-up", SetID: setID}
			ack, err := owner.Run(requestContext, "AddCommunicationToCase", "local-author", input)
			if err == nil || ack != nil || !errors.Is(requestContext.Err(), cause) {
				t.Fatalf("interrupted write returned an acknowledgment or lost its cause: %v, %v", err, requestContext.Err())
			}
			if len(observed) != 5 || !slices.Equal(observed[:4], []string{id, input.Body, "00FF", "02C9"}) || observed[4] == "" {
				t.Fatalf("interrupted before observing the complete communication and both files: %d values", len(observed))
			}
			if err := host.Close(); err != nil {
				t.Fatal(err)
			}
			resumedHost, resumed := openTeam(t, path)
			if !slices.Equal(supportRows(t, resumedHost.Database()), before) || !slices.Equal(supportRows(t, peerHost.Database()), peerBefore) {
				t.Fatal("interrupted transaction changed saved owner or peer rows after reopen")
			}
			wantFault(t, resumed, "DescribeAttachment", Input{AttachmentID: observed[4]}, "AttachmentIdNotFound")
			call(t, resumed, "AddCommunicationToCase", input)
			messages := call(t, resumed, "DescribeCommunications", Input{CaseID: id})["communications"].([]map[string]any)
			if len(messages) != 2 || messages[0]["body"] != input.Body || messages[1]["body"] != "Original" {
				t.Fatal("retry did not produce exactly one new communication")
			}
			attachments := messages[0]["attachmentSet"].([]map[string]string)
			if len(attachments) != len(files) {
				t.Fatal("retry produced the wrong number of attachments")
			}
			for i, detail := range attachments {
				stored := call(t, resumed, "DescribeAttachment", Input{AttachmentID: detail["attachmentId"]})["attachment"].(Attachment)
				if detail["attachmentId"] == observed[4] || stored.Name != files[i].Name || !bytes.Equal(stored.Data, files[i].Data) {
					t.Fatal("retry did not save a fresh attachment with its original bytes")
				}
			}
			retained := call(t, resumed, "DescribeCommunications", Input{CaseID: sibling})["communications"].([]map[string]any)
			if len(retained) != 1 || retained[0]["body"] != "Retained" || !slices.Equal(supportRows(t, peerHost.Database()), peerBefore) {
				t.Fatal("retry changed a sibling case or the separate team")
			}
		})
	}
}
