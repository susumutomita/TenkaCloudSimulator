package aws

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/susumutomita/TenkaCloudSimulator/internal/core"
)

func isCloudFrontVpcOrigin(r *http.Request) bool {
	if r.URL.Path != cloudFrontPath && !strings.HasPrefix(r.URL.Path, cloudFrontPath+"/") {
		return false
	}
	if service := r.Header.Get("X-Simulator-Service"); service != "" {
		return service == "cloudfront"
	}
	return elbSigningService(r.Header.Get("Authorization")) == "cloudfront"
}

func cfError(w http.ResponseWriter, err error) {
	f := cfFailure{500, "InternalError", "CloudFront state could not be accessed."}
	var serviceErr cfFailure
	if errors.As(err, &serviceErr) {
		f = serviceErr
	}
	body := struct {
		XMLName xml.Name `xml:"ErrorResponse"`
		XMLNS   string   `xml:"xmlns,attr"`
		Code    string   `xml:"Error>Code"`
		Message string   `xml:"Error>Message"`
	}{XMLNS: cloudFrontNS, Code: f.code, Message: f.message}
	writeXML(w, f.status, body)
}

func cfRequestAction(r *http.Request) (action, id string, err error) {
	if r.URL.RawQuery != "" || r.URL.RawPath != "" || r.Header.Get("X-Amz-Target") != "" {
		return "", "", cfInvalid("Use the native VPC origin REST path without query parameters.")
	}
	if r.URL.Path == cloudFrontPath && r.Method == "POST" {
		return "CreateVpcOrigin", "", nil
	}
	id = strings.TrimPrefix(r.URL.Path, cloudFrontPath+"/")
	if !cloudFrontOriginID.MatchString(id) {
		return "", "", cfInvalid("Unsupported VPC origin path.")
	}
	switch r.Method {
	case "GET":
		return "GetVpcOrigin", id, nil
	case "PUT":
		return "UpdateVpcOrigin", id, nil
	case "DELETE":
		return "DeleteVpcOrigin", id, nil
	}
	return "", "", cfInvalid("Unsupported VPC origin operation.")
}

func cfCaller(r *http.Request) (string, error) {
	for _, h := range []string{"Authorization", "X-Simulator-Service", "X-Simulator-Region", "X-Simulator-Principal", "X-Amz-Target", "X-Amz-Security-Token", "If-Match"} {
		if len(r.Header.Values(h)) > 1 {
			return "", cfInvalid("Repeated routing, identity and precondition headers are unsupported.")
		}
	}
	if r.Header.Get("X-Amz-Security-Token") != "" {
		return "", cfFailure{403, "AccessDenied", "Session credentials are unsupported."}
	}
	if a := r.Header.Get("Authorization"); a != "" {
		_, after, _ := strings.Cut(a, "Credential=")
		cred, _, _ := strings.Cut(after, ",")
		parts := strings.Split(cred, "/")
		if !strings.HasPrefix(a, "AWS4-HMAC-SHA256 ") || len(parts) != 5 || !strings.HasPrefix(parts[0], "TCSIM") || parts[1] == "" || parts[2] != "us-east-1" || parts[3] != "cloudfront" || parts[4] != "aws4_request" {
			return "", cfFailure{403, "AccessDenied", "Use dummy TCSIM credentials signed for cloudfront in us-east-1."}
		}
	}
	if region := r.Header.Get("X-Simulator-Region"); region != "" && region != "us-east-1" {
		return "", cfInvalid("The CloudFront control endpoint is global; use us-east-1.")
	}
	if p := r.Header.Get("X-Simulator-Principal"); p != "" {
		return p, nil
	}
	return core.Administrator, nil
}

// Native control operations run entirely against local state. Authentication
// is applied by the common server before selecting this provider.
func (s *CloudFrontStore) ServeHTTP(g *Gateway, w http.ResponseWriter, r *http.Request) {
	action, id, err := cfRequestAction(r)
	if err != nil {
		cfError(w, err)
		return
	}
	principal, err := cfCaller(r)
	if err != nil {
		cfError(w, err)
		return
	}
	if s == nil || g.Identities == nil {
		cfError(w, errors.New("missing stores"))
		return
	}
	resource := "*"
	if id != "" {
		resource = cloudFrontARNPrefix + id
	}
	authorize := func(action, resource string) bool {
		decision, err := g.Identities.DecisionContext(r.Context(), principal, "cloudfront:"+action, resource)
		if err != nil {
			cfError(w, err)
			return false
		}
		w.Header().Set("X-Simulator-IAM-Decision", decision)
		if decision != "allowed" {
			cfError(w, cfFailure{403, "AccessDenied", "The selected identity cannot perform this CloudFront operation."})
			return false
		}
		return true
	}
	if !authorize(action, resource) {
		return
	}
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 32<<10))
	if err != nil {
		cfError(w, cfInvalid("The VPC origin request is unreadable or exceeds 32 KiB."))
		return
	}
	if (action == "GetVpcOrigin" || action == "DeleteVpcOrigin") && len(data) > 0 {
		cfError(w, cfInvalid("This operation does not accept a request body."))
		return
	}
	var o CFVpcOrigin
	status := http.StatusAccepted
	switch action {
	case "CreateVpcOrigin", "UpdateVpcOrigin":
		c, tags, e := parseCFOriginRequest(data, action == "CreateVpcOrigin")
		if e != nil {
			cfError(w, e)
			return
		}
		if action == "CreateVpcOrigin" {
			id, err = newCFOriginID()
			if err != nil {
				cfError(w, err)
				return
			}
			if len(tags) > 0 && !authorize("TagResource", cloudFrontARNPrefix+id) {
				return
			}
			o, err = s.save(r.Context(), id, "", true, c, tags)
			if err == nil {
				w.Header().Set("Location", cloudFrontPath+"/"+o.ID)
			}
		} else {
			o, err = s.Update(r.Context(), id, r.Header.Get("If-Match"), c)
		}
	case "DeleteVpcOrigin":
		o, err = s.Delete(r.Context(), id, r.Header.Get("If-Match"))
	case "GetVpcOrigin":
		o, err = s.Get(r.Context(), id)
		status = http.StatusOK
	}
	if err != nil {
		cfError(w, err)
		return
	}
	w.Header().Set("ETag", o.ETag)
	writeXML(w, status, o)
}

var cfXMLFields = map[string][]string{
	"CreateVpcOriginRequest":  {"VpcOriginEndpointConfig", "Tags"},
	"VpcOriginEndpointConfig": {"Name", "Arn", "HTTPPort", "HTTPSPort", "OriginProtocolPolicy", "OriginSslProtocols"},
	"OriginSslProtocols":      {"Items", "Quantity"},
	"Tags":                    {"Items"}, "Tag": {"Key", "Value"},
}

func parseCFOriginRequest(data []byte, create bool) (CFOriginConfig, []CFOriginTag, error) {
	root := "VpcOriginEndpointConfig"
	if create {
		root = "CreateVpcOriginRequest"
	}
	if err := checkCFXML(data, root); err != nil {
		return CFOriginConfig{}, nil, err
	}
	var c CFOriginConfig
	var tags []CFOriginTag
	var err error
	if create {
		var req struct {
			Config CFOriginConfig `xml:"VpcOriginEndpointConfig"`
			Tags   []CFOriginTag  `xml:"Tags>Items>Tag"`
		}
		err = xml.Unmarshal(data, &req)
		c, tags = req.Config, req.Tags
	} else {
		err = xml.Unmarshal(data, &c)
	}
	if err != nil {
		return CFOriginConfig{}, nil, cfInvalid("Invalid VPC origin configuration values.")
	}
	return c, tags, nil
}

// Reject unknown fields and duplicate scalar values before encoding/xml's
// typed decoding can discard either. Only explicit list members can repeat.
func checkCFXML(data []byte, root string) error {
	type frame struct {
		name string
		seen map[string]bool
	}
	var stack []frame
	closed := false
	invalid := cfInvalid("Malformed or unsupported CloudFront XML field.")
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		t, err := dec.Token()
		if err == io.EOF && closed {
			return nil
		}
		if err != nil {
			return invalid
		}
		switch n := t.(type) {
		case xml.StartElement:
			name := n.Name.Local
			if n.Name.Space != "" && n.Name.Space != cloudFrontNS {
				return invalid
			}
			if len(stack) == 0 {
				if closed || name != root {
					return invalid
				}
			} else {
				top := stack[len(stack)-1]
				allowed := false
				repeated := false
				if top.name == "Items" && len(stack) >= 2 {
					grand := stack[len(stack)-2].name
					repeated = (grand == "Tags" && name == "Tag") || (grand == "OriginSslProtocols" && name == "SslProtocol")
					allowed = repeated
				} else {
					for _, f := range cfXMLFields[top.name] {
						if f == name {
							allowed = true
							break
						}
					}
				}
				if !allowed || (!repeated && top.seen[name]) {
					return invalid
				}
				top.seen[name] = true
			}
			for _, a := range n.Attr {
				if len(stack) != 0 || a.Name.Space != "" || a.Name.Local != "xmlns" || a.Value != cloudFrontNS {
					return invalid
				}
			}
			stack = append(stack, frame{name, map[string]bool{}})
		case xml.EndElement:
			stack = stack[:len(stack)-1]
			closed = len(stack) == 0
		case xml.CharData:
			if strings.TrimSpace(string(n)) != "" && (len(stack) == 0 || cfXMLFields[stack[len(stack)-1].name] != nil || stack[len(stack)-1].name == "Items") {
				return invalid
			}
		case xml.ProcInst:
			if n.Target != "xml" || closed || len(stack) > 0 {
				return invalid
			}
		case xml.Comment:
		default:
			return invalid
		}
	}
}
