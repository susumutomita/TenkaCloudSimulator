package supportcases

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"
)

func TestEmptyAttachmentRoundTrip(t *testing.T) {
	_, s := openTeam(t, filepath.Join(t.TempDir(), "owned.sqlite"))
	set := call(t, s, "AddAttachmentsToSet", Input{Attachments: []Attachment{{Name: "empty.txt", Data: []byte{}}}})["attachmentSetId"].(string)
	id := call(t, s, "CreateCase", Input{Subject: "Synthetic empty file", Body: "initial", SetID: set})["caseId"].(string)
	comm := call(t, s, "DescribeCommunications", Input{CaseID: id})["communications"].([]map[string]any)[0]
	fid := comm["attachmentSet"].([]map[string]string)[0]["attachmentId"]
	file := call(t, s, "DescribeAttachment", Input{AttachmentID: fid})["attachment"].(Attachment)
	encoded, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `{"fileName":"empty.txt","data":""}` {
		t.Errorf("empty output: %s", encoded)
	}
}

func TestCommunicationTimeRangeIncludesExactBounds(t *testing.T) {
	_, s := openTeam(t, filepath.Join(t.TempDir(), "owned.sqlite"))
	stamp := time.Date(2026, 11, 28, 8, 0, 0, 123456000, time.UTC)
	s.now = func() time.Time { return stamp }
	id := call(t, s, "CreateCase", Input{Subject: "Synthetic range", Body: "at the exact stored microsecond"})["caseId"].(string)
	count := func(after, before string) int {
		return len(call(t, s, "DescribeCommunications", Input{CaseID: id, AfterTime: after, BeforeTime: before})["communications"].([]map[string]any))
	}
	for _, tc := range []struct {
		name, after, before string
		want                int
	}{
		{"exact lower", stamp.Format(time.RFC3339Nano), "", 1},
		{"one ns above as lower", stamp.Add(time.Nanosecond).Format(time.RFC3339Nano), "", 0},
		{"exact upper", "", stamp.Format(time.RFC3339Nano), 1},
		{"one ns below as upper", "", stamp.Add(-time.Nanosecond).Format(time.RFC3339Nano), 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if n := count(tc.after, tc.before); n != tc.want {
				t.Errorf("received %d row, expected %d", n, tc.want)
			}
		})
	}
	t.Run("inverted interval with calendar zero", func(t *testing.T) {
		_, err := s.Run(context.Background(), "DescribeCommunications", "local-author", Input{CaseID: id, AfterTime: stamp.Format(time.RFC3339Nano), BeforeTime: "0001-01-01T00:00:00Z"})
		if err == nil {
			t.Error("inverted inclusive time interval accepted")
		}
	})
}
