package aws

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

type S3Tag struct{ Key, Value string }

func validTagText(value string) bool {
	if !utf8.ValidString(value) {
		return false
	}
	for _, c := range value {
		if !unicode.IsLetter(c) && !unicode.IsNumber(c) && !unicode.Is(unicode.Z, c) && !strings.ContainsRune("_.:/=+-@", c) {
			return false
		}
	}
	return true
}
func validateS3Tags(tags []S3Tag) error {
	if len(tags) > 50 {
		return core.Fail("InvalidTag", "A bucket supports at most 50 user tags.")
	}
	seen := map[string]bool{}
	for _, tag := range tags {
		if seen[tag.Key] || utf8.RuneCountInString(tag.Key) < 1 || utf8.RuneCountInString(tag.Key) > 128 || utf8.RuneCountInString(tag.Value) > 256 || !validTagText(tag.Key) || !validTagText(tag.Value) || strings.HasPrefix(strings.ToLower(tag.Key), "aws:") {
			return core.Fail("InvalidTag", "Tag keys must be unique, 1–128 Unicode characters; values up to 256. Use letters, numbers, spaces or _.:/=+-@; aws: is reserved.")
		}
		seen[tag.Key] = true
	}
	return nil
}

// Validate structure while decoding so duplicate, unknown and nested fields are
// rejected. Key/value whitespace is data and is never trimmed.
func parseS3Tags(body []byte) ([]S3Tag, error) {
	dec := xml.NewDecoder(bytes.NewReader(body))
	stack := []string{}
	tags := []S3Tag{}
	var tag S3Tag
	root, closed, tagSet := false, false, false
	fields := map[string]bool{}
	malformed := func() ([]S3Tag, error) {
		return nil, core.Fail("MalformedXML", "Expected Tagging/TagSet/Tag with exactly one Key and Value.")
	}
	for {
		token, err := dec.Token()
		if err == io.EOF {
			if !closed || !tagSet {
				return malformed()
			}
			return tags, validateS3Tags(tags)
		}
		if err != nil {
			return malformed()
		}
		path := strings.Join(stack, "/")
		switch t := token.(type) {
		case xml.StartElement:
			if closed || (t.Name.Space != "" && t.Name.Space != ns) {
				return malformed()
			}
			for _, a := range t.Attr {
				if root || a.Name.Local != "xmlns" {
					return malformed()
				}
			}
			switch path {
			case "":
				if root || t.Name.Local != "Tagging" {
					return malformed()
				}
				root = true
			case "Tagging":
				if tagSet || t.Name.Local != "TagSet" {
					return malformed()
				}
				tagSet = true
			case "Tagging/TagSet":
				if t.Name.Local != "Tag" {
					return malformed()
				}
				tag = S3Tag{}
				fields = map[string]bool{}
			case "Tagging/TagSet/Tag":
				if (t.Name.Local != "Key" && t.Name.Local != "Value") || fields[t.Name.Local] {
					return malformed()
				}
				fields[t.Name.Local] = true
			default:
				return malformed()
			}
			stack = append(stack, t.Name.Local)
		case xml.EndElement:
			if path == "Tagging/TagSet/Tag" {
				if !fields["Key"] || !fields["Value"] {
					return malformed()
				}
				tags = append(tags, tag)
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				closed = true
			}
		case xml.CharData:
			switch path {
			case "Tagging/TagSet/Tag/Key":
				tag.Key += string(t)
			case "Tagging/TagSet/Tag/Value":
				tag.Value += string(t)
			default:
				if strings.TrimSpace(string(t)) != "" {
					return malformed()
				}
			}
		case xml.ProcInst:
			if root || t.Target != "xml" {
				return malformed()
			}
		case xml.Comment:
		default:
			return malformed()
		}
	}
}
func s3WriteTags(tx *sql.Tx, bucket string, tags []S3Tag) error {
	if err := validateS3Tags(tags); err != nil {
		return err
	}
	if len(tags) == 0 {
		_, err := tx.Exec("DELETE FROM aws_s3_tags WHERE bucket=?", bucket)
		return err
	}
	ordered := append([]S3Tag(nil), tags...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Key < ordered[j].Key })
	body, _ := json.Marshal(ordered)
	_, err := tx.Exec("INSERT INTO aws_s3_tags VALUES(?,?) ON CONFLICT(bucket) DO UPDATE SET document=excluded.document", bucket, string(body))
	return err
}
func (s *S3Store) Tags(bucket string) ([]S3Tag, error) {
	var raw sql.NullString
	err := s.db.QueryRow("SELECT t.document FROM buckets b LEFT JOIN aws_s3_tags t ON b.name=t.bucket WHERE b.name=?", bucket).Scan(&raw)
	if err == sql.ErrNoRows {
		return nil, core.Fail("NoSuchBucket", "The specified bucket does not exist.")
	}
	if err != nil {
		return nil, err
	}
	if !raw.Valid {
		return nil, core.Fail("NoSuchTagSet", "The bucket has no tag set.")
	}
	var tags []S3Tag
	if json.Unmarshal([]byte(raw.String), &tags) != nil || len(tags) == 0 || validateS3Tags(tags) != nil {
		return nil, core.Fail("InternalError", "Bucket tag state could not be read.")
	}
	return tags, nil
}
func (s *S3Store) SetTags(bucket string, tags []S3Tag) error {
	if err := validateS3Tags(tags); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s3BucketExists(tx, bucket); err != nil {
		return err
	}
	if err = s3WriteTags(tx, bucket, tags); err != nil {
		return err
	}
	return tx.Commit()
}
func (g *Gateway) bucketTags(w http.ResponseWriter, r *http.Request, bucket string) {
	if g.S3 == nil {
		fail(w, r, "InternalError", "S3 tag state is unavailable.")
		return
	}
	switch r.Method {
	case "GET":
		tags, err := g.S3.Tags(bucket)
		if err != nil {
			fromError(w, r, err)
			return
		}
		writeXML(w, 200, struct {
			XMLName xml.Name `xml:"Tagging"`
			XMLNS   string   `xml:"xmlns,attr"`
			Tags    []S3Tag  `xml:"TagSet>Tag"`
		}{XMLNS: ns, Tags: tags})
	case "PUT":
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
		if err != nil {
			fail(w, r, "MalformedXML", "Tagging exceeds 64 KiB.")
			return
		}
		if err = checksums(r, body); err != nil {
			fail(w, r, "BadDigest", err.Error())
			return
		}
		checksum := false
		for _, name := range []string{"Content-MD5", "x-amz-checksum-sha1", "x-amz-checksum-sha256", "x-amz-checksum-crc32", "x-amz-checksum-crc32c"} {
			checksum = checksum || r.Header.Get(name) != ""
		}
		if !checksum {
			fail(w, r, "InvalidRequest", "Provide a supported checksum for bucket tagging.")
			return
		}
		tags, err := parseS3Tags(body)
		if err != nil {
			fromError(w, r, err)
			return
		}
		if err = g.S3.SetTags(bucket, tags); err != nil {
			fromError(w, r, err)
			return
		}
		w.WriteHeader(200)
	case "DELETE":
		if err := g.S3.SetTags(bucket, nil); err != nil {
			fromError(w, r, err)
			return
		}
		w.WriteHeader(204)
	default:
		fail(w, r, "MethodNotAllowed", "Use GET, PUT or DELETE for bucket tags.")
	}
}
