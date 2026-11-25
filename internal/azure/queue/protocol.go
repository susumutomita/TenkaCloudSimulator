// Package queue implements a bounded local Azure Queue Storage REST data plane.
package queue

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const apiVersion = "2023-11-03"
const maxMessage = 64 << 10
const maxRequest = 512 << 10
const week = 7 * 24 * 60 * 60

type wireMessage struct {
	ID       string  `xml:"MessageId"`
	Inserted string  `xml:"InsertionTime"`
	Expires  string  `xml:"ExpirationTime"`
	Receipt  string  `xml:"PopReceipt,omitempty"`
	Visible  string  `xml:"TimeNextVisible,omitempty"`
	Count    int64   `xml:"DequeueCount,omitempty"`
	Text     *string `xml:"MessageText,omitempty"`
}

// MarshalXML preserves a present zero count for Peek/Get payload responses.
// Send responses have no payload and must not acquire an unrelated count field.
func (m wireMessage) MarshalXML(encoder *xml.Encoder, start xml.StartElement) error {
	type fields wireMessage
	out := struct {
		fields
		Count *int64 `xml:"DequeueCount,omitempty"`
	}{fields: fields(m)}
	if m.Text != nil {
		out.Count = &m.Count
	}
	return encoder.EncodeElement(out, start)
}

type messageList struct {
	XMLName  xml.Name      `xml:"QueueMessagesList"`
	Messages []wireMessage `xml:"QueueMessage"`
}
type apiError struct {
	Status  int
	Code    string
	Message string
}

func (e *apiError) Error() string           { return e.Message }
func problem(status int, code string) error { return &apiError{status, code, code} }
func respondError(w http.ResponseWriter, err error) {
	var e *apiError
	if !errors.As(err, &e) {
		e = &apiError{500, "InternalError", "Local storage operation failed"}
	}
	respondXML(w, e.Status, struct {
		XMLName xml.Name `xml:"Error"`
		Code    string   `xml:"Code"`
		Message string   `xml:"Message"`
	}{Code: e.Code, Message: e.Message})
}
func respondXML(w http.ResponseWriter, status int, v any) {
	body, err := xml.Marshal(v)
	if err != nil {
		http.Error(w, "XML serialization failed", 500)
		return
	}
	w.Header().Set("Content-Type", "application/xml")
	w.WriteHeader(status)
	_, _ = w.Write(append([]byte(xml.Header), body...))
}
func opaque() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
func messageID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:]), nil
}
func stamp(t int64) string { return time.Unix(t, 0).UTC().Format(http.TimeFormat) }
func integer(q url.Values, key string, def, min, max int64) (int64, error) {
	s, ok := q[key]
	if !ok {
		return def, nil
	}
	if len(s) != 1 || s[0] == "" {
		return 0, problem(400, "InvalidQueryParameterValue")
	}
	n, err := strconv.ParseInt(s[0], 10, 64)
	if err != nil || n < min || n > max {
		return 0, problem(400, "InvalidQueryParameterValue")
	}
	return n, nil
}

// textBody refuses ambiguous XML (unknown fields/attributes, directives and
// nested markup). Empty HTTP body is permitted only for visibility-only update.
func textBody(r io.Reader, optional bool) (*string, error) {
	d := xml.NewDecoder(io.LimitReader(r, maxRequest+1))
	depth := 0
	root := false
	seen := false
	closed := false
	var text strings.Builder
	total := int64(0)
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, problem(400, "InvalidXmlDocument")
		}
		total = d.InputOffset()
		if total > maxRequest {
			return nil, problem(413, "RequestBodyTooLarge")
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if closed || len(t.Attr) > 0 || t.Name.Space != "" {
				return nil, problem(400, "InvalidXmlDocument")
			}
			if depth == 0 && t.Name.Local == "QueueMessage" && !root {
				root = true
			} else if depth == 1 && t.Name.Local == "MessageText" && !seen {
				seen = true
			} else {
				return nil, problem(400, "InvalidXmlDocument")
			}
			depth++
		case xml.EndElement:
			depth--
			if depth == 0 {
				closed = true
			}
		case xml.CharData:
			if depth == 2 {
				text.Write(t)
			} else if strings.TrimSpace(string(t)) != "" {
				return nil, problem(400, "InvalidXmlDocument")
			}
		case xml.ProcInst:
			if root || t.Target != "xml" {
				return nil, problem(400, "InvalidXmlDocument")
			}
		case xml.Directive:
			return nil, problem(400, "InvalidXmlDocument")
		}
	}
	if !root && optional && total == 0 {
		return nil, nil
	}
	if !root || !seen || !closed || depth != 0 {
		return nil, problem(400, "InvalidXmlDocument")
	}
	s := text.String()
	if len(s) > maxMessage {
		return nil, problem(400, "RequestBodyTooLarge")
	}
	return &s, nil
}
