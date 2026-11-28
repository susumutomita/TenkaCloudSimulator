package catalog

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/susumutomita/TenkaCloudSimulator/internal/aws/kinesisvideo"
)

var (
	videoStreamName    = regexp.MustCompile(`^[a-zA-Z0-9_.-]{1,256}$`)
	videoStreamARN     = regexp.MustCompile(`^arn:aws:kinesisvideo:[a-z0-9-]+:[0-9]{12}:stream/[a-zA-Z0-9_.-]{1,256}/[0-9]+$`)
	videoUnixTimestamp = regexp.MustCompile(`^(0|[1-9][0-9]{0,11})(\.[0-9]{1,9})?$`)
)

func (e *Engine) serveKinesisVideoMedia(w http.ResponseWriter, r *http.Request) bool {
	if restService(r) != "kinesisvideo" || len(e.bySigning["kinesisvideo"]) == 0 {
		return false
	}
	var op string
	switch r.URL.Path {
	case "/getDataEndpoint":
		op = "GetDataEndpoint"
	case "/getMedia":
		op = "GetMedia"
	case "/putMedia":
		op = "PutMedia"
	default:
		return false
	}
	b := e.bySigning["kinesisvideo"][0]
	err := e.videoRequest(w, r, b, op)
	if err != nil {
		code, status, message := "InternalFailure", http.StatusInternalServerError, "The local video operation could not be completed."
		var f *fault
		if errors.As(err, &f) {
			code, status, message = f.code, http.StatusBadRequest, f.message
			switch {
			case f.kind == "denied" || code == "NotAuthorizedException":
				code, status = "NotAuthorizedException", http.StatusUnauthorized
			case code == "ResourceNotFoundException":
				status = http.StatusNotFound
			case code == "":
				code = "InvalidArgumentException"
			}
		}
		writeError(w, b.model, code, status, message)
	}
	return true
}

// The service address comes from the actual accepted socket. The client Host
// and forwarded headers cannot select the endpoint returned to another SDK.
func videoEndpoint(r *http.Request) (string, error) {
	addr, ok := r.Context().Value(http.LocalAddrContextKey).(*net.TCPAddr)
	if !ok || !addr.IP.IsLoopback() || addr.Port <= 0 || addr.Zone != "" {
		return "", Failure("InvalidArgumentException", "A numeric loopback listener is required for local media.")
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + net.JoinHostPort(addr.IP.String(), strconv.Itoa(addr.Port)), nil
}

func videoTimestamp(text string) (int64, error) {
	if stamp, err := time.Parse(time.RFC3339Nano, text); err == nil && stamp.UnixMilli() >= 0 {
		return stamp.UnixMilli(), nil
	}
	if !videoUnixTimestamp.MatchString(text) {
		return 0, Failure("InvalidArgumentException", "Use a UNIX timestamp in seconds.")
	}
	seconds, fraction, _ := strings.Cut(text, ".")
	base, err := strconv.ParseInt(seconds, 10, 64)
	if err != nil {
		return 0, err
	}
	ms, _ := strconv.ParseInt((fraction + "000")[:3], 10, 64)
	return base*1000 + ms, nil
}

func (e *Engine) videoOwner(w http.ResponseWriter, r *http.Request, region, op, name, arn string) (row, error) {
	if (name == "") == (arn == "") || (name != "" && !videoStreamName.MatchString(name)) || (arn != "" && (len(arn) > 1024 || !videoStreamARN.MatchString(arn))) {
		return row{}, Failure("InvalidArgumentException", "Supply exactly one valid stream name or ARN.")
	}
	column, key := "arn", arn
	guess := arn
	if name != "" {
		column, key = "name", name
		guess = fmt.Sprintf("arn:aws:kinesisvideo:%s:%s:stream/%s/*", region, e.cfg.Account, name)
	}
	owner, err := scanRow(e.store.db.QueryRowContext(r.Context(), "SELECT "+columns+" FROM aws_catalog_resources WHERE service='kinesisvideo' AND region=? AND kind='stream' AND parent='' AND "+column+"=?", region, key))
	missing := errors.Is(err, sql.ErrNoRows)
	if err != nil && !missing {
		return row{}, err
	}
	if !missing {
		guess = owner.ARN
	}
	principal := r.Header.Get("X-Simulator-Principal")
	if principal == "" {
		principal = e.cfg.DefaultPrincipal
	}
	decision, err := e.cfg.Authorize(r.Context(), principal, "kinesisvideo:"+op, guess)
	if err != nil {
		return row{}, err
	}
	w.Header().Set("X-Simulator-IAM-Decision", decision)
	if decision != "allowed" {
		return row{}, Failure("NotAuthorizedException", "The caller cannot access this stream.")
	}
	if missing {
		return row{}, Failure("ResourceNotFoundException", "The stream does not exist.")
	}
	return owner, nil
}

func (e *Engine) videoRequest(w http.ResponseWriter, r *http.Request, b *binding, op string) error {
	if r.Method != http.MethodPost || r.URL.RawQuery != "" {
		return Failure("InvalidArgumentException", "Use a POST without query parameters.")
	}
	region, err := e.region(r, b)
	if err != nil {
		return err
	}
	if op == "PutMedia" {
		return e.putVideoMedia(w, r, region)
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<10))
	if err != nil {
		return Failure("InvalidArgumentException", "Video request exceeds the local input limit.")
	}
	obj, err := decodeJSONObject(raw)
	if err != nil {
		return err
	}
	name, err := videoStringField(obj, "StreamName")
	if err != nil {
		return err
	}
	arn, err := videoStringField(obj, "StreamARN")
	if err != nil {
		return err
	}
	owner, err := e.videoOwner(w, r, region, op, name, arn)
	if err != nil {
		return err
	}
	delete(obj, "StreamName")
	delete(obj, "StreamARN")
	if op == "GetDataEndpoint" {
		api, err := videoStringField(obj, "APIName")
		if err != nil || len(obj) != 1 || (api != "PUT_MEDIA" && api != "GET_MEDIA") {
			return Failure("InvalidArgumentException", "Local video supports PUT_MEDIA and GET_MEDIA endpoints.")
		}
		tx, err := e.store.db.BeginTx(r.Context(), &sql.TxOptions{ReadOnly: true})
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err = currentVideoStream(r.Context(), tx, owner); err != nil {
			return err
		}
		endpoint, err := videoEndpoint(r)
		if err != nil {
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		w.Header().Set("Content-Type", "application/json")
		return json.NewEncoder(w).Encode(map[string]string{"DataEndpoint": endpoint})
	}
	sel, ok := obj["StartSelector"].(map[string]any)
	if len(obj) != 1 || !ok {
		return Failure("InvalidArgumentException", "A local media start selector is required.")
	}
	mode, err := videoStringField(sel, "StartSelectorType")
	if err != nil {
		return err
	}
	var cursor string
	switch mode {
	case "EARLIEST":
		if len(sel) != 1 {
			return Failure("InvalidArgumentException", "EARLIEST accepts no secondary selector.")
		}
	case "CONTINUATION_TOKEN":
		cursor, err = videoStringField(sel, "ContinuationToken")
		if err != nil || len(cursor) != 32 || len(sel) != 2 {
			return Failure("InvalidArgumentException", "Supply the returned continuation token.")
		}
	default:
		return Failure("InvalidArgumentException", "Local media supports EARLIEST and CONTINUATION_TOKEN.")
	}
	chunks, err := e.readVideoFragments(r.Context(), owner, cursor)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "video/x-matroska")
	w.WriteHeader(http.StatusOK)
	for _, chunk := range chunks {
		if _, err := w.Write(chunk); err != nil {
			return nil
		} // The peer stopped reading the already committed response.
	}
	return nil
}

func videoStringField(obj map[string]any, key string) (string, error) {
	v, present := obj[key]
	if !present {
		return "", nil
	}
	s, ok := v.(string)
	if !ok || s == "" {
		return "", Failure("InvalidArgumentException", "Invalid "+key+".")
	}
	return s, nil
}

func (e *Engine) putVideoMedia(w http.ResponseWriter, r *http.Request, region string) error {
	for _, h := range []string{"X-Amzn-Stream-Name", "X-Amzn-Stream-Arn", "X-Amzn-Fragment-Timecode-Type", "X-Amzn-Producer-Start-Timestamp"} {
		if len(r.Header.Values(h)) > 1 {
			return Failure("InvalidArgumentException", "Repeated video header.")
		}
	}
	owner, err := e.videoOwner(w, r, region, "PutMedia", r.Header.Get("X-Amzn-Stream-Name"), r.Header.Get("X-Amzn-Stream-Arn"))
	if err != nil {
		return err
	}
	if r.Header.Get("X-Amzn-Fragment-Timecode-Type") != "RELATIVE" {
		return Failure("InvalidArgumentException", "The local producer accepts relative fragment timecodes.")
	}
	start, err := videoTimestamp(r.Header.Get("X-Amzn-Producer-Start-Timestamp"))
	if err != nil {
		return err
	}
	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, kinesisvideo.MaxFragmentBytes))
	if err != nil {
		return Failure("InvalidArgumentException", "Invalid or oversized finite video upload.")
	}
	receipt, err := e.saveVideoFragment(r.Context(), owner, start, payload)
	if err != nil {
		return err
	}
	events := []string{"BUFFERING", "RECEIVED"}
	if receipt.number != 0 {
		events = append(events, "PERSISTED")
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	if flusher != nil {
		flusher.Flush()
	}
	enc := json.NewEncoder(w)
	for _, event := range events {
		ack := map[string]any{"EventType": event, "FragmentTimecode": receipt.producerMS - start}
		if receipt.number != 0 {
			ack["FragmentNumber"] = strconv.FormatInt(receipt.number, 10)
		}
		if err := enc.Encode(ack); err != nil {
			return nil
		}
		if flusher != nil {
			flusher.Flush()
		}
	}
	return nil
}
