package appengine

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash/fnv"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	pythonruntime "github.com/susumutomita/TenkaCloudSimulator/internal/workload/python"
)

//go:embed wsgi.py
var adapterSource []byte

// DataPlanePrefix is the provider-relative mount of deployed applications:
// /_simulator/providers/gcp/appspot/<official hostname>/<path>.
const DataPlanePrefix = "/_simulator/providers/gcp/appspot/"

// Data-plane bounds. They are local limits, not App Engine quotas.
const (
	maxRequestBodyBytes = 64 << 10
	maxHeaderBytes      = 32 << 10
)

// dataPlaneSecurity keeps deployed HTML in an opaque, script-free sandbox:
// an application never runs code with the simulator origin's authority.
const dataPlaneSecurity = "default-src 'none'; img-src data:; style-src 'unsafe-inline'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'; sandbox"

type target struct {
	service, version string
	explicit         bool
}

// parseHost maps an official App Engine hostname to a service and version:
// PROJECT, SERVICE-dot-PROJECT or VERSION-dot-SERVICE-dot-PROJECT, either as
// .appspot.com or as the regional .REGION_ID.r.appspot.com form. A
// two-part name is a service, or else a version of the default service.
func (e *Engine) parseHost(tx *sql.Tx, host, location string) (target, bool, error) {
	host = strings.ToLower(host)
	rest, ok := strings.CutSuffix(host, ".appspot.com")
	if !ok {
		return target{}, false, nil
	}
	l, _ := findLocation(location)
	rest = strings.TrimSuffix(rest, "."+l.RegionID+".r")
	parts := strings.Split(rest, "-dot-")
	if parts[len(parts)-1] != e.cfg.Project {
		return target{}, false, nil
	}
	switch len(parts) {
	case 1:
		return target{service: "default"}, true, nil
	case 2:
		if _, err := loadService(tx, e.cfg.Project, parts[0]); err == nil {
			return target{service: parts[0]}, true, nil
		} else if !isNotFound(err) {
			return target{}, false, err
		}
		if _, err := loadVersion(tx, e.cfg.Project, "default", parts[0]); err == nil {
			return target{service: "default", version: parts[0], explicit: true}, true, nil
		} else if !isNotFound(err) {
			return target{}, false, err
		}
		return target{}, false, nil
	case 3:
		return target{service: parts[1], version: parts[0], explicit: true}, true, nil
	}
	return target{}, false, nil
}

// choose maps a request to a version of the split: IP hashes the client
// address, COOKIE reads the GOOGAPPUID cookie and RANDOM draws a number.
func choose(s serviceRecord, r *http.Request, address netip.Addr) (string, *http.Cookie) {
	var point int
	var cookie *http.Cookie
	switch s.ShardBy {
	case "COOKIE":
		point = -1
		if c, err := r.Cookie("GOOGAPPUID"); err == nil {
			if n, err := strconv.Atoi(c.Value); err == nil && n >= 0 && n < allocationUnits {
				point = n
			}
		}
		if point < 0 {
			point = randomPoint()
			// Like the simulator session cookie, Secure follows the
			// connection: a plain-HTTP loopback client would drop it.
			cookie = &http.Cookie{Name: "GOOGAPPUID", Value: strconv.Itoa(point), HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode}
		}
	case "RANDOM":
		point = randomPoint()
	default:
		h := fnv.New64a()
		_, _ = h.Write([]byte(s.ID + "|" + address.String()))
		point = int(h.Sum64() % allocationUnits)
	}
	for _, id := range sortedAllocations(s.Allocations) {
		point -= s.Allocations[id]
		if point < 0 {
			return id, cookie
		}
	}
	return "", cookie
}

func randomPoint() int {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return int(binary.BigEndian.Uint64(b[:]) % allocationUnits)
}

func plain(w http.ResponseWriter, code int, message string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(code)
	_, _ = io.WriteString(w, message+"\n")
}

// forwarded lists request headers the application receives. Simulator
// credentials, cookies and hop-by-hop headers never reach guest code.
func forwarded(r *http.Request) ([][2]string, bool) {
	out, size := [][2]string{}, 0
	for name, values := range r.Header {
		lower := strings.ToLower(name)
		switch {
		case strings.HasPrefix(lower, "x-simulator-"), strings.HasPrefix(lower, "x-tenkacloud-"), strings.HasPrefix(lower, "proxy-"),
			lower == "authorization", lower == "cookie", lower == "connection", lower == "keep-alive", lower == "te", lower == "trailer",
			lower == "transfer-encoding", lower == "upgrade", lower == "content-length", lower == "x-amz-security-token":
			continue
		}
		for _, value := range values {
			out = append(out, [2]string{name, value})
			size += len(name) + len(value)
		}
	}
	cookies := []string{}
	for _, c := range r.Cookies() {
		if !strings.HasPrefix(c.Name, "tcsim_") {
			cookies = append(cookies, c.Name+"="+c.Value)
		}
	}
	if len(cookies) > 0 {
		value := strings.Join(cookies, "; ")
		out = append(out, [2]string{"Cookie", value})
		size += len(value)
	}
	return out, size <= maxHeaderBytes
}

var headerName = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]+$")

// returned lists the response headers an application may set. Cookies,
// caching, security policy and cross-origin headers stay with the simulator.
var returned = map[string]bool{"content-type": true, "content-language": true, "content-disposition": true, "etag": true, "last-modified": true, "vary": true}

func (e *Engine) serve(w http.ResponseWriter, r *http.Request, escaped []string) {
	w.Header().Set("Content-Security-Policy", dataPlaneSecurity)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	if len(escaped) == 0 || escaped[0] == "" {
		plain(w, http.StatusNotFound, "Name an App Engine hostname: "+DataPlanePrefix+"<PROJECT_ID>.<REGION_ID>.r.appspot.com/")
		return
	}
	subject, ok := e.cfg.Subject(r.Context())
	if !ok || subject == "" {
		plain(w, http.StatusUnauthorized, "An authenticated simulator session is required.")
		return
	}
	host := escaped[0]
	mount := DataPlanePrefix + host
	tail := strings.TrimPrefix(r.URL.EscapedPath(), "/appspot/"+host)
	path, err := url.PathUnescape(tail)
	if err != nil {
		plain(w, http.StatusBadRequest, "The request path is not valid.")
		return
	}
	if path == "" {
		path = "/"
	}
	address, err := clientAddress(r.RemoteAddr)
	if err != nil {
		plain(w, http.StatusForbidden, "Forbidden: the client address is unknown.")
		return
	}
	type selected struct {
		version version
		cookie  *http.Cookie
	}
	result, err := e.read(r.Context(), func(tx *sql.Tx) (any, error) {
		a, err := loadApplication(tx, e.cfg.Project)
		if isNotFound(err) {
			return nil, &status{"NOT_FOUND", "The requested application does not exist."}
		} else if err != nil {
			return nil, err
		}
		t, found, err := e.parseHost(tx, host, a.Location)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, &status{"NOT_FOUND", "The requested URL was not found on this server."}
		}
		if a.ServingStatus != "SERVING" {
			return nil, &status{"UNAVAILABLE", "The application is disabled."}
		}
		rules, err := loadRules(tx, e.cfg.Project)
		if err != nil {
			return nil, err
		}
		if !allowed(rules, address) {
			return nil, &status{"PERMISSION_DENIED", "Forbidden: the App Engine firewall denied this client address."}
		}
		s, err := loadService(tx, e.cfg.Project, t.service)
		if isNotFound(err) {
			return nil, &status{"NOT_FOUND", "The requested URL was not found on this server."}
		} else if err != nil {
			return nil, err
		}
		var cookie *http.Cookie
		if !t.explicit {
			t.version, cookie = choose(s, r, address)
		}
		v, err := loadVersion(tx, e.cfg.Project, t.service, t.version)
		if isNotFound(err) {
			return nil, &status{"NOT_FOUND", "The requested URL was not found on this server."}
		} else if err != nil {
			return nil, err
		}
		if v.ServingStatus != "SERVING" {
			return nil, &status{"UNAVAILABLE", "The version " + v.ID + " of service " + v.Service + " is stopped."}
		}
		return selected{v, cookie}, nil
	})
	if err != nil {
		var s *status
		if !errors.As(err, &s) {
			plain(w, http.StatusInternalServerError, "The simulator could not read the application state.")
			return
		}
		plain(w, httpCodes[s.code], s.message)
		return
	}
	chosen := result.(selected)
	if chosen.cookie != nil {
		chosen.cookie.Path = mount + "/"
		http.SetCookie(w, chosen.cookie)
	}
	headers, fits := forwarded(r)
	if !fits {
		plain(w, http.StatusRequestHeaderFieldsTooLarge, "Request headers exceed the local 32 KiB limit.")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxRequestBodyBytes))
	if err != nil {
		plain(w, http.StatusRequestEntityTooLarge, "Request bodies are limited locally to 64 KiB.")
		return
	}
	e.invoke(w, r, chosen.version, mount, path, headers, body, address)
}

func (e *Engine) invoke(w http.ResponseWriter, r *http.Request, v version, mount, path string, headers [][2]string, body []byte, address netip.Addr) {
	code, err := bundle(v.Code)
	if err != nil {
		plain(w, http.StatusInternalServerError, "The stored version could not be prepared.")
		return
	}
	serverName, serverPort, err := net.SplitHostPort(r.Host)
	if err != nil {
		serverName, serverPort = r.Host, "80"
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	event, _ := json.Marshal(map[string]any{"method": r.Method, "path": path, "query": r.URL.RawQuery, "headers": headers,
		"body": base64.StdEncoding.EncodeToString(body), "scriptName": mount, "serverName": serverName, "serverPort": serverPort,
		"scheme": scheme, "remoteAddr": address.String()})
	environment := map[string]string{}
	for k, value := range v.Environment {
		environment[k] = value
	}
	for k, value := range map[string]string{"GAE_ENV": "standard", "GAE_RUNTIME": v.Runtime, "GAE_SERVICE": v.Service, "GAE_VERSION": v.ID,
		"GOOGLE_CLOUD_PROJECT": e.cfg.Project, "PORT": "8080"} {
		environment[k] = value
	}
	runner, err := e.python(r.Context())
	if err != nil {
		plain(w, http.StatusServiceUnavailable, "The local Python runtime is not available.")
		return
	}
	result, err := runner.Run(r.Context(), pythonruntime.Invocation{Code: code, Handler: "_tcsim_appengine.handle", Event: event, Environment: environment,
		Context: map[string]string{"service": v.Service, "version": v.ID}, Timeout: e.cfg.RequestTimeout})
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		plain(w, http.StatusGatewayTimeout, "The request exceeded the local "+e.cfg.RequestTimeout.String()+" deadline.")
		return
	case err != nil:
		plain(w, http.StatusBadGateway, "The version did not return a response: "+err.Error())
		return
	case result.FunctionError:
		var failure struct{ ErrorType, ErrorMessage string }
		_ = json.Unmarshal(result.Payload, &failure)
		plain(w, http.StatusInternalServerError, "Internal Server Error: the application raised "+failure.ErrorType+": "+failure.ErrorMessage)
		return
	}
	var response struct {
		Status  string      `json:"status"`
		Headers [][2]string `json:"headers"`
		Body    string      `json:"body"`
	}
	if json.Unmarshal(result.Payload, &response) != nil {
		plain(w, http.StatusBadGateway, "The version returned an invalid WSGI response.")
		return
	}
	codeText, _, _ := strings.Cut(response.Status, " ")
	statusCode, err := strconv.Atoi(codeText)
	data, decodeErr := base64.StdEncoding.DecodeString(response.Body)
	if err != nil || len(codeText) != 3 || statusCode < 200 || statusCode > 599 || decodeErr != nil {
		plain(w, http.StatusBadGateway, "The version returned an invalid WSGI status or body.")
		return
	}
	for _, header := range response.Headers {
		name, value := header[0], header[1]
		if !headerName.MatchString(name) || strings.ContainsAny(value, "\r\n\x00") || !returned[strings.ToLower(name)] {
			continue
		}
		w.Header().Add(name, value)
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	w.Header().Set("X-Simulator-AppEngine-Version", v.Service+"/"+v.ID)
	w.WriteHeader(statusCode)
	if r.Method != http.MethodHead {
		_, _ = w.Write(data)
	}
}

// RequestTimeout is the data-plane deadline of one request.
func (e *Engine) RequestTimeout() time.Duration { return e.cfg.RequestTimeout }
