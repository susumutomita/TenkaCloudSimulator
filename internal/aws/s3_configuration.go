package aws

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"strings"
)

type publicAccessBlock struct {
	XMLName               xml.Name `xml:"PublicAccessBlockConfiguration" json:"-"`
	XMLNS                 string   `xml:"xmlns,attr,omitempty" json:"-"`
	BlockPublicAcls       bool
	IgnorePublicAcls      bool
	BlockPublicPolicy     bool
	RestrictPublicBuckets bool
}

// encoding/xml enforces matching start/end names. This small state machine adds
// a single root, known scalar fields, no duplicate fields and bounded callers.
func s3XMLFields(body []byte, root string, allowed map[string]bool) (map[string]string, error) {
	dec := xml.NewDecoder(bytes.NewReader(body))
	fields := map[string]string{}
	opened, closed := false, false
	field, value := "", ""
	for {
		token, err := dec.Token()
		if err == io.EOF {
			if closed {
				return fields, nil
			}
			return nil, io.ErrUnexpectedEOF
		}
		if err != nil {
			return nil, err
		}
		switch t := token.(type) {
		case xml.StartElement:
			if closed || (t.Name.Space != "" && t.Name.Space != ns) {
				return nil, io.ErrUnexpectedEOF
			}
			if !opened {
				if t.Name.Local != root {
					return nil, io.ErrUnexpectedEOF
				}
				for _, a := range t.Attr {
					if a.Name.Local != "xmlns" {
						return nil, io.ErrUnexpectedEOF
					}
				}
				opened = true
			} else {
				if field != "" || !allowed[t.Name.Local] || len(t.Attr) != 0 {
					return nil, io.ErrUnexpectedEOF
				}
				if _, duplicate := fields[t.Name.Local]; duplicate {
					return nil, io.ErrUnexpectedEOF
				}
				field, value = t.Name.Local, ""
			}
		case xml.EndElement:
			if field != "" {
				fields[field] = strings.TrimSpace(value)
				field = ""
			} else {
				closed = true
			}
		case xml.CharData:
			if field != "" {
				value += string(t)
			} else if strings.TrimSpace(string(t)) != "" {
				return nil, io.ErrUnexpectedEOF
			}
		case xml.ProcInst:
			if opened || closed || t.Target != "xml" {
				return nil, io.ErrUnexpectedEOF
			}
		case xml.Comment:
		default:
			return nil, io.ErrUnexpectedEOF
		}
	}
}
func (g *Gateway) bucketConfiguration(w http.ResponseWriter, r *http.Request, bucket, kind string) {
	if g.S3 == nil {
		fail(w, r, "InternalError", "S3 version state is unavailable.")
		return
	}
	if r.Method != "GET" && r.Method != "PUT" && !(r.Method == "DELETE" && kind == "publicAccessBlock") {
		fail(w, r, "MethodNotAllowed", "Unsupported bucket configuration method.")
		return
	}
	// Read/validate bounded input before taking the shared write transaction. A
	// slow configuration client must not hold the sole state connection hostage.
	status, publicJSON := "", ""
	if r.Method == "PUT" {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
		if err != nil {
			fail(w, r, "MalformedXML", "Configuration exceeds 4 KiB.")
			return
		}
		if err = checksums(r, body); err != nil {
			fail(w, r, "BadDigest", err.Error())
			return
		}
		if kind == "versioning" {
			fields, e := s3XMLFields(body, "VersioningConfiguration", map[string]bool{"Status": true, "MfaDelete": true})
			if e != nil {
				fail(w, r, "MalformedXML", "Expected a supported VersioningConfiguration.")
				return
			}
			if _, ok := fields["MfaDelete"]; ok {
				fail(w, r, "NotImplemented", "MFA Delete is unsupported.")
				return
			}
			status = fields["Status"]
			if status != "Enabled" && status != "Suspended" {
				fail(w, r, "MalformedXML", "Status must be Enabled or Suspended.")
				return
			}
		} else {
			allowed := map[string]bool{"BlockPublicAcls": true, "IgnorePublicAcls": true, "BlockPublicPolicy": true, "RestrictPublicBuckets": true}
			fields, e := s3XMLFields(body, "PublicAccessBlockConfiguration", allowed)
			if e != nil {
				fail(w, r, "MalformedXML", "Expected a supported PublicAccessBlockConfiguration.")
				return
			}
			values := map[string]bool{}
			for k, value := range fields {
				if value != "true" && value != "false" {
					fail(w, r, "MalformedXML", "Public access block values must be true or false.")
					return
				}
				values[k] = value == "true"
			}
			raw, _ := json.Marshal(publicAccessBlock{BlockPublicAcls: values["BlockPublicAcls"], IgnorePublicAcls: values["IgnorePublicAcls"], BlockPublicPolicy: values["BlockPublicPolicy"], RestrictPublicBuckets: values["RestrictPublicBuckets"]})
			publicJSON = string(raw)
		}
	}
	tx, err := g.S3.db.Begin()
	if err != nil {
		fromError(w, r, err)
		return
	}
	defer tx.Rollback()
	state, err := s3State(tx, bucket)
	if err != nil {
		fromError(w, r, err)
		return
	}
	if r.Method == "GET" {
		if kind == "versioning" {
			writeXML(w, 200, struct {
				XMLName xml.Name `xml:"VersioningConfiguration"`
				XMLNS   string   `xml:"xmlns,attr"`
				Status  string   `xml:",omitempty"`
			}{XMLNS: ns, Status: state})
			return
		}
		var raw sql.NullString
		err = tx.QueryRow("SELECT public_access FROM aws_s3_config WHERE bucket=?", bucket).Scan(&raw)
		if err == sql.ErrNoRows || (err == nil && !raw.Valid) {
			fail(w, r, "NoSuchPublicAccessBlockConfiguration", "The public access block configuration was not found.")
			return
		}
		if err != nil {
			fromError(w, r, err)
			return
		}
		var conf publicAccessBlock
		if json.Unmarshal([]byte(raw.String), &conf) != nil {
			fail(w, r, "InternalError", "Public access block state could not be read.")
			return
		}
		conf.XMLNS = ns
		writeXML(w, 200, conf)
		return
	}
	if r.Method == "DELETE" {
		_, err = tx.Exec("UPDATE aws_s3_config SET public_access=NULL WHERE bucket=?", bucket)
	} else if kind == "versioning" {
		_, err = tx.Exec("INSERT INTO aws_s3_config(bucket,status) VALUES(?,?) ON CONFLICT(bucket) DO UPDATE SET status=excluded.status", bucket, status)
	} else {
		_, err = tx.Exec("INSERT INTO aws_s3_config(bucket,status,public_access) VALUES(?,'',?) ON CONFLICT(bucket) DO UPDATE SET public_access=excluded.public_access", bucket, publicJSON)
	}
	if err == nil {
		err = tx.Commit()
	}
	if err != nil {
		fromError(w, r, err)
		return
	}
	if r.Method == "DELETE" {
		w.WriteHeader(204)
	} else {
		w.WriteHeader(200)
	}
}
