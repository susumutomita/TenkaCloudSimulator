package aws

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
)

// Only metadata: ListDocumentVersions is a separate document-scoped permission,
// not a grant to read definition content, approval state, or command runtime.
type ssmDocumentVersionInfo struct {
	Name             string
	DocumentVersion  string
	CreatedDate      float64
	DocumentFormat   string
	IsDefaultVersion bool
	Status           string
}

func (s *SSMDocumentStore) documentVersionPage(r *http.Request, region, principal, name string, size int, token string) (any, error) {
	// One SQLite statement yields one snapshot. The parent row distinguishes a
	// missing document from an inconsistent or incomplete version set. Don't load
	// document bodies just to enumerate (or fingerprint) their metadata.
	rows, err := s.db.QueryContext(r.Context(), `SELECT d.latest,v.version,v.created
		FROM aws_ssm_documents AS d LEFT JOIN aws_ssm_document_versions AS v
		ON v.region=d.region AND v.name=d.name
		WHERE d.region=? AND d.name=? ORDER BY v.version LIMIT ?`, region, name, ssmDocumentVersionLimit+1)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []ssmDocumentVersionInfo
	var latest int
	for rows.Next() {
		var version int
		var created float64
		if err = rows.Scan(&latest, &version, &created); err != nil {
			return nil, err
		}
		if latest < 1 || latest > ssmDocumentVersionLimit || version != len(items)+1 || version > latest {
			return nil, fmt.Errorf("inconsistent SSM document version history")
		}
		items = append(items, ssmDocumentVersionInfo{
			Name: name, DocumentVersion: strconv.Itoa(version), CreatedDate: created,
			DocumentFormat: "JSON", IsDefaultVersion: version == 1, Status: "Active",
		})
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, documentFailure("InvalidDocument", "Document does not exist.")
	}
	if len(items) != latest {
		return nil, fmt.Errorf("incomplete SSM document version history")
	}
	// Keep cursor domains disjoint, even if another operation changes its row shape.
	snapshot, err := json.Marshal(struct {
		Operation string
		Versions  []ssmDocumentVersionInfo
	}{"ListDocumentVersions", items})
	if err != nil {
		return nil, err
	}
	start, end, next, err := ssmDocumentPageWindow(snapshot, region, principal, size, token, len(items))
	if err != nil {
		return nil, err
	}
	response := map[string]any{"DocumentVersions": items[start:end]}
	if next != "" {
		response["NextToken"] = next
	}
	return response, nil
}
