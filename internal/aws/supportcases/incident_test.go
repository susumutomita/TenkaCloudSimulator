package supportcases

import (
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestIncidentClassificationIsOptionalAtomicAndCaseBound(t *testing.T) {
	path := filepath.Join(t.TempDir(), "support.db")
	host, s := openTeam(t, path)
	opened := time.Date(2025, 11, 28, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return opened }
	in := Input{Subject: "Same subject", Body: "First note"}
	ordinary := call(t, s, "CreateCase", in)["caseId"].(string)
	in.ServiceCode = "sentinel-report-incident"
	classified := call(t, s, "CreateCase", in)["caseId"].(string)
	before := supportRows(t, host.Database())

	// The metadata is the last write. Refusal must undo the newly inserted
	// case and its first message, while retaining both pre-existing cases.
	if _, err := host.Database().Exec(`CREATE TEMP TRIGGER refuse_incident BEFORE INSERT ON aws_support_case_services
		BEGIN SELECT RAISE(IGNORE); END`); err != nil {
		t.Fatal(err)
	}
	if result, err := s.Run(t.Context(), "CreateCase", "local-author", in); err == nil || result != nil {
		t.Fatal("acknowledged an unstored incident", result, err)
	}
	if _, err := host.Database().Exec("DROP TRIGGER refuse_incident"); err != nil {
		t.Fatal(err)
	}
	in.ServiceCode = "sentinel-report-service-request"
	wantFault(t, s, "CreateCase", in, "ValidationException")
	if !slices.Equal(before, supportRows(t, host.Database())) {
		t.Fatal("failed incident changed case, message or attachment rows")
	}
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	host, s = openTeam(t, path)
	s.now = func() time.Time { return opened }
	wantCodes := func(expected map[string]string) {
		t.Helper()
		items := call(t, s, "DescribeCases", Input{IncludeResolved: true})["cases"].([]map[string]any)
		if len(items) != len(expected) {
			t.Fatalf("case count %d, want %d", len(items), len(expected))
		}
		for _, item := range items {
			id := item["caseId"].(string)
			want, ok := expected[id]
			code, present := item["serviceCode"]
			if !ok || present != (want != "") || (present && code != want) {
				t.Fatalf("wrong serviceCode for %q: %v", id, item)
			}
		}
	}
	wantCodes(map[string]string{ordinary: "", classified: "sentinel-report-incident"})
	call(t, s, "AddCommunicationToCase", Input{CaseID: classified, Body: "Follow-up"})
	call(t, s, "ResolveCase", Input{CaseID: classified})
	wantCodes(map[string]string{ordinary: "", classified: "sentinel-report-incident"})

	// At the retention boundary the incident still exists. One microsecond
	// later the next successful write removes its persisted classification.
	s.now = func() time.Time { return opened.AddDate(1, 0, 0) }
	wantCodes(map[string]string{ordinary: "", classified: "sentinel-report-incident"})
	s.now = func() time.Time { return opened.AddDate(1, 0, 0).Add(time.Microsecond) }
	last := call(t, s, "CreateCase", Input{Subject: "Another case", Body: "Later"})["caseId"].(string)
	wantCodes(map[string]string{last: ""})
	var remaining int
	if err := host.Database().QueryRow("SELECT COUNT(*) FROM aws_support_case_services").Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("expired incident metadata survived", remaining, err)
	}
}
