package qga

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strings"
)

type request struct {
	Execute   string `json:"execute"`
	Arguments any    `json:"arguments"`
	ID        int64  `json:"id"`
}

type commandArguments struct {
	Path    string   `json:"path"`
	Args    []string `json:"arg,omitempty"`
	Input   string   `json:"input-data,omitempty"`
	Capture bool     `json:"capture-output"`
}

func execArguments(c Command) commandArguments {
	return commandArguments{Path: c.Path, Args: c.Args, Input: base64.StdEncoding.EncodeToString(c.Stdin), Capture: true}
}

// exchange runs with gate held. All IO is bounded by the same call deadline.
// Cancellation closes the stream and waits for that callback before releasing
// the slot, so a late callback can never close a subsequent successful RPC.
func (c *Client) exchange(ctx context.Context, name string, arguments any, nonce *int64) (data json.RawMessage, sent bool, err error) {
	if err = ctx.Err(); err != nil {
		return nil, false, err
	}
	if c.nextID == math.MaxInt64 {
		_ = c.Close()
		return nil, false, fmt.Errorf("%w: request ID exhausted", ErrLimit)
	}
	c.nextID++
	id := c.nextID
	wire, err := json.Marshal(request{Execute: name, Arguments: arguments, ID: id})
	if err != nil {
		return nil, false, err
	}
	if nonce != nil {
		wire = append([]byte{0xff}, wire...)
	}
	wire = append(wire, '\n')
	if len(wire) > MaxRequestBytes {
		return nil, false, fmt.Errorf("%w: request", ErrLimit)
	}
	deadline, _ := ctx.Deadline()
	if err = c.transport.SetDeadline(deadline); err != nil {
		_ = c.Close()
		return nil, false, err
	}
	cancelDone := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = c.Close(); close(cancelDone) })
	defer func() {
		if !stop() {
			<-cancelDone
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		if err == nil {
			select {
			case <-c.done:
				err = ErrClosed
			default:
			}
		}
		var remote *RemoteError
		if err != nil && !errors.As(err, &remote) {
			_ = c.Close()
		}
	}()
	if err = ctx.Err(); err != nil {
		return nil, false, err
	}
	var n int
	n, err = c.transport.Write(wire)
	sent = n > 0
	if err != nil {
		return nil, sent, err
	}
	if n != len(wire) {
		return nil, sent, io.ErrShortWrite
	}
	if nonce != nil {
		data, err = c.readSync(id, *nonce)
	} else {
		var frame []byte
		frame, err = c.readFrame()
		if err == nil {
			data, err = parseEnvelope(frame, id)
		}
	}
	return data, sent, err
}

func (c *Client) readFrame() ([]byte, error) {
	frame := make([]byte, 0, 1024)
	for {
		b, err := c.reader.ReadByte()
		if err != nil {
			if err == io.EOF && len(frame) > 0 {
				err = io.ErrUnexpectedEOF
			}
			return nil, err
		}
		if b == '\n' {
			return frame, nil
		}
		if len(frame) == MaxFrameBytes {
			return nil, fmt.Errorf("%w: response frame", ErrLimit)
		}
		frame = append(frame, b)
	}
}

// QGA's delimiter resets the parser. Discard stale bytes and replies until a
// delimited reply matches both the request ID and the random int64 echo. The
// entire scan, including malformed stale data and repeated delimiters, is capped.
func (c *Client) readSync(id, nonce int64) (json.RawMessage, error) {
	var frame []byte
	active := false
	for consumed := 0; consumed < MaxSyncBytes; consumed++ {
		b, err := c.reader.ReadByte()
		if err != nil {
			return nil, err
		}
		if b == 0xff {
			active = true
			frame = frame[:0]
			continue
		}
		if !active {
			continue
		}
		if b == '\n' {
			data, parseErr := parseEnvelope(frame, id)
			var echo int64
			if parseErr == nil && json.Unmarshal(data, &echo) == nil && string(data) != "null" && echo == nonce {
				return data, nil
			}
			// Even an error with this request ID may predate the new connection.
			// Only the random echo establishes synchronization.
			active = false
			continue
		}
		if len(frame) == MaxFrameBytes {
			return nil, fmt.Errorf("%w: sync frame", ErrLimit)
		}
		frame = append(frame, b)
	}
	return nil, fmt.Errorf("%w: stale sync stream", ErrLimit)
}

// object rejects duplicate fields (encoding/json otherwise silently overwrites
// them), null, non-object roots, and trailing values. Concrete schemas reject
// unknown fields rather than silently accepting a new protocol interpretation.
func object(data []byte, allowed ...string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrProtocol
	}
	fields := make(map[string]json.RawMessage)
	for decoder.More() {
		token, err = decoder.Token()
		if err != nil {
			return nil, ErrProtocol
		}
		key, ok := token.(string)
		if !ok {
			return nil, ErrProtocol
		}
		if _, duplicate := fields[key]; duplicate {
			return nil, ErrProtocol
		}
		known := false
		for _, name := range allowed {
			if name == key {
				known = true
				break
			}
		}
		if !known {
			return nil, ErrProtocol
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, ErrProtocol
		}
		fields[key] = value
	}
	if _, err = decoder.Token(); err != nil {
		return nil, ErrProtocol
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, ErrProtocol
	}
	return fields, nil
}

func field(fields map[string]json.RawMessage, key string, dst any, required bool) error {
	raw, ok := fields[key]
	if !ok && !required {
		return nil
	}
	if !ok || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, dst) != nil {
		return ErrProtocol
	}
	return nil
}

func parseEnvelope(frame []byte, wantID int64) (json.RawMessage, error) {
	fields, err := object(frame, "id", "return", "error")
	if err != nil {
		return nil, err
	}
	var id int64
	if field(fields, "id", &id, true) != nil || id != wantID {
		return nil, fmt.Errorf("%w: response ID", ErrProtocol)
	}
	result, returned := fields["return"]
	remote, rejected := fields["error"]
	if returned == rejected {
		return nil, ErrProtocol
	}
	if returned {
		if bytes.Equal(bytes.TrimSpace(result), []byte("null")) {
			return nil, ErrProtocol
		}
		return result, nil
	}
	errorFields, err := object(remote, "class", "desc")
	if err != nil {
		return nil, err
	}
	e := &RemoteError{}
	if field(errorFields, "class", &e.Class, true) != nil || field(errorFields, "desc", &e.Description, true) != nil || e.Class == "" {
		return nil, ErrProtocol
	}
	return nil, e
}

func parsePID(data []byte) (int64, error) {
	fields, err := object(data, "pid")
	if err != nil {
		return 0, err
	}
	var pid int64
	if field(fields, "pid", &pid, true) != nil || pid <= 0 {
		return 0, ErrProtocol
	}
	return pid, nil
}

func parseStatus(data []byte) (Status, error) {
	fields, err := object(data, "exited", "exitcode", "signal", "out-data", "err-data", "out-truncated", "err-truncated")
	if err != nil {
		return Status{}, err
	}
	var result Status
	if field(fields, "exited", &result.Exited, true) != nil {
		return Status{}, ErrProtocol
	}
	if !result.Exited {
		if len(fields) != 1 {
			return Status{}, ErrProtocol
		}
		return result, nil
	}
	_, code := fields["exitcode"]
	_, signal := fields["signal"]
	if code == signal {
		return result, ErrProtocol
	}
	// Preserve signed QAPI int values, including Windows high-bit process
	// status stored in gint. POSIX-only sign restrictions are not portable.
	var value int64
	if code {
		if field(fields, "exitcode", &value, true) != nil {
			return result, ErrProtocol
		}
		result.ExitCode = &value
	} else {
		if field(fields, "signal", &value, true) != nil {
			return result, ErrProtocol
		}
		result.Signal = &value
	}
	var outTruncated, errTruncated bool
	if field(fields, "out-truncated", &outTruncated, false) != nil || field(fields, "err-truncated", &errTruncated, false) != nil {
		return result, ErrProtocol
	}
	var stdout, stderr string
	if field(fields, "out-data", &stdout, false) != nil || field(fields, "err-data", &stderr, false) != nil {
		return result, ErrProtocol
	}
	if outTruncated || errTruncated {
		return result, ErrOutputTruncated
	}
	result.Stdout, err = decodeOutput(stdout, MaxOutputBytes)
	if err != nil {
		return result, err
	}
	result.Stderr, err = decodeOutput(stderr, MaxOutputBytes-len(result.Stdout))
	return result, err
}

func decodeOutput(encoded string, remaining int) ([]byte, error) {
	size := base64.StdEncoding.DecodedLen(len(encoded))
	if strings.HasSuffix(encoded, "==") {
		size -= 2
	} else if strings.HasSuffix(encoded, "=") {
		size--
	}
	if size > remaining {
		return nil, fmt.Errorf("%w: decoded output", ErrLimit)
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || base64.StdEncoding.EncodeToString(decoded) != encoded {
		return nil, fmt.Errorf("%w: output base64", ErrProtocol)
	}
	return decoded, nil
}
