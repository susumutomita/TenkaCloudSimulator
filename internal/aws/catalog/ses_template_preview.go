package catalog

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var sesTemplateKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var sesTemplateDelimiter = regexp.MustCompile(`\{\{|\}\}`)

// TemplateData is a JSON string on the SES wire. Read its members separately
// so duplicate keys cannot silently overwrite substitutions.
func sesTemplateValues(data string) (map[string]string, error) {
	bad := invalid("TemplateData must be one JSON object with unique keys and string values.")
	dec := json.NewDecoder(strings.NewReader(data))
	first, err := dec.Token()
	if err != nil || first != json.Delim('{') {
		return nil, bad
	}
	values := map[string]string{}
	for dec.More() {
		key, err := dec.Token()
		if err != nil {
			return nil, bad
		}
		name, ok := key.(string)
		if _, duplicate := values[name]; !ok || duplicate {
			return nil, bad
		}
		token, err := dec.Token()
		s, ok := token.(string)
		if err != nil || !ok {
			return nil, bad
		}
		values[name] = s
	}
	if end, err := dec.Token(); err != nil || end != json.Delim('}') {
		return nil, bad
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, bad
	}
	return values, nil
}

func fillSESTemplate(ctx context.Context, source string, values map[string]string) (string, error) {
	var out strings.Builder
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		tag := sesTemplateDelimiter.FindStringIndex(source)
		if tag == nil {
			if out.Len()+len(source) > maxRequestBytes {
				return "", invalid("Rendered template exceeds the local preview limit.")
			}
			out.WriteString(source)
			return out.String(), nil
		}
		if source[tag[0]:tag[1]] != "{{" || (tag[0] > 0 && source[tag[0]-1] == '\\') {
			return "", invalid("Unsupported SES template tag syntax.")
		}
		start := tag[1]
		close := strings.Index(source[start:], "}}")
		if close < 0 {
			return "", invalid("Unclosed SES template tag.")
		}
		end := start + close
		name := strings.Trim(source[start:end], " ")
		if !sesTemplateKey.MatchString(name) {
			return "", invalid("SES preview supports simple named template tags only.")
		}
		value, ok := values[name]
		if !ok {
			return "", invalid("TemplateData is missing a referenced value.")
		}
		if out.Len()+tag[0]+len(value) > maxRequestBytes {
			return "", invalid("Rendered template exceeds the local preview limit.")
		}
		out.WriteString(source[:tag[0]])
		out.WriteString(value)
		source = source[end+2:]
	}
}

// Encode entire subjects, including literal RFC 2047-looking text. A 42-byte
// chunk fits an encoded word on the first header line; fold only between runes.
func writeSESSubject(out *strings.Builder, subject string) {
	out.WriteString("Subject: ")
	for len(subject) > 0 {
		n := min(42, len(subject))
		for n < len(subject) && !utf8.RuneStart(subject[n]) {
			n--
		}
		out.WriteString("=?UTF-8?B?")
		out.WriteString(base64.StdEncoding.EncodeToString([]byte(subject[:n])))
		out.WriteString("?=")
		subject = subject[n:]
		if subject != "" {
			out.WriteString("\r\n ")
		}
	}
	out.WriteString("\r\n")
}

// Each 57-byte input block produces at most 76 base64 characters (RFC 2045).
func writeSESPart(out *strings.Builder, mediaType, body string) {
	out.WriteString("Content-Type: " + mediaType + "; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n")
	for len(body) > 0 {
		n := min(57, len(body))
		out.WriteString(base64.StdEncoding.EncodeToString([]byte(body[:n])))
		out.WriteString("\r\n")
		body = body[n:]
	}
}

func renderSESTemplate(ctx context.Context, content, data any) (map[string]any, error) {
	rawData, ok := data.(string)
	if !ok {
		return nil, invalid("TemplateData must be a JSON string.")
	}
	values, err := sesTemplateValues(rawData)
	if err != nil {
		return nil, err
	}
	template, _ := content.(map[string]any)
	rendered := map[string]string{}
	for _, field := range []string{"Subject", "Text", "Html"} {
		source, present := template[field]
		if !present {
			continue
		}
		s, ok := source.(string)
		if !ok {
			return nil, invalid("Stored SES template content must contain strings.")
		}
		rendered[field], err = fillSESTemplate(ctx, s, values)
		if err != nil {
			return nil, err
		}
	}
	plain, hasPlain := rendered["Text"]
	html, hasHTML := rendered["Html"]
	if !hasPlain && !hasHTML {
		return nil, invalid("SES preview requires a text or HTML body.")
	}
	var out strings.Builder
	if subject, ok := rendered["Subject"]; ok {
		if strings.ContainsFunc(subject, unicode.IsControl) {
			return nil, invalid("SES preview subjects cannot contain control characters.")
		}
		writeSESSubject(&out, subject)
	}
	out.WriteString("MIME-Version: 1.0\r\n")
	switch {
	case hasPlain && hasHTML:
		// The hyphenated boundary cannot occur within a base64-encoded body.
		const boundary = "ses-preview-alternative"
		out.WriteString("Content-Type: multipart/alternative; boundary=" + boundary + "\r\n\r\n--" + boundary + "\r\n")
		writeSESPart(&out, "text/plain", plain)
		out.WriteString("\r\n--" + boundary + "\r\n")
		writeSESPart(&out, "text/html", html)
		out.WriteString("\r\n--" + boundary + "--\r\n")
	case hasPlain:
		writeSESPart(&out, "text/plain", plain)
	default:
		writeSESPart(&out, "text/html", html)
	}
	if out.Len() > maxRequestBytes {
		return nil, invalid("Rendered MIME exceeds the local preview limit.")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"RenderedTemplate": out.String()}, nil
}
