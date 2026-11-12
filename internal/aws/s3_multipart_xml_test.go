package aws

import (
	"strings"
	"testing"
)

func TestS3MultipartCompletionGrammarIsExact(t *testing.T) {
	part := `<Part><PartNumber>1</PartNumber><ETag>"abc"</ETag></Part>`
	for name, body := range map[string]string{
		"plain":       `<CompleteMultipartUpload>` + part + `</CompleteMultipartUpload>`,
		"namespace":   `<CompleteMultipartUpload xmlns="http://s3.amazonaws.com/doc/2006-03-01/">` + part + `</CompleteMultipartUpload>`,
		"declaration": `<?xml version="1.0" encoding="UTF-8"?><CompleteMultipartUpload>` + part + `</CompleteMultipartUpload>`,
		"comment":     `<CompleteMultipartUpload><!-- note -->` + part + `</CompleteMultipartUpload>`,
		"whitespace":  "<CompleteMultipartUpload>\n  " + part + "\n</CompleteMultipartUpload>\n",
	} {
		parts, err := multipartXML([]byte(body))
		if err != nil || len(parts) != 1 || parts[0].Number != 1 || parts[0].ETag != `"abc"` {
			t.Errorf("%s: %v %v", name, parts, err)
		}
	}
	for name, body := range map[string]string{
		"syntax":           `<CompleteMultipartUpload><Part>`,
		"unclosed":         `<CompleteMultipartUpload>` + part,
		"foreign attr":     `<CompleteMultipartUpload a="b">` + part + `</CompleteMultipartUpload>`,
		"part attr":        `<CompleteMultipartUpload><Part a="b"><PartNumber>1</PartNumber><ETag>x</ETag></Part></CompleteMultipartUpload>`,
		"other child":      `<CompleteMultipartUpload><Other/></CompleteMultipartUpload>`,
		"deep nesting":     `<CompleteMultipartUpload><Part><PartNumber><X/></PartNumber></Part></CompleteMultipartUpload>`,
		"stray text":       `<CompleteMultipartUpload>text` + part + `</CompleteMultipartUpload>`,
		"late declaration": `<CompleteMultipartUpload>` + part + `<?xml version="1.0"?></CompleteMultipartUpload>`,
		"other procinst":   `<?xml-stylesheet href="x"?><CompleteMultipartUpload>` + part + `</CompleteMultipartUpload>`,
		"trailing doc":     `<CompleteMultipartUpload>` + part + `</CompleteMultipartUpload><CompleteMultipartUpload/>`,
		"missing ETag":     `<CompleteMultipartUpload><Part><PartNumber>1</PartNumber></Part></CompleteMultipartUpload>`,
		"bad number":       `<CompleteMultipartUpload><Part><PartNumber>one</PartNumber><ETag>x</ETag></Part></CompleteMultipartUpload>`,
		"DTD":              `<!DOCTYPE x><CompleteMultipartUpload>` + part + `</CompleteMultipartUpload>`,
		"too many parts":   `<CompleteMultipartUpload>` + strings.Repeat(part, 10001) + `</CompleteMultipartUpload>`,
	} {
		if parts, err := multipartXML([]byte(body)); err == nil {
			t.Errorf("%s accepted: %v", name, parts)
		}
	}
}
