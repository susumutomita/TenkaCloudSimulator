package cognitoidp

import (
	"bytes"
	"html/template"
)

// The only untrusted page content is a URL that acs has matched to a live
// registration before adding the grant result. html/template escapes the
// link's URL/attribute context. The script is fixed text, never interpolated
// from the URL or SAML data; the link also works without JavaScript.
var hostedCompletionDocument = template.Must(template.New("cognito-complete").Parse(`<!doctype html>
<html lang="en">
<head><meta charset="utf-8" /><title>Sign-in complete</title></head>
<body>
<main>
<h1>Sign-in complete</h1>
<p><a id="callback" rel="noreferrer" href="{{.Callback}}">Continue to the application</a></p>
</main>
<script nonce="{{.Nonce}}">window.location.replace(document.getElementById("callback").href);</script>
</body>
</html>
`))

func hostedCompletionHTML(nonce, callback string) ([]byte, error) {
	var output bytes.Buffer
	if err := hostedCompletionDocument.Execute(&output, struct{ Nonce, Callback string }{nonce, callback}); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
