package python

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
)

// Exchange is a trusted, context-bound capability selected by the embedding
// provider. The guest controls only the message, never its authority or target.
// Implementations must honor cancellation and must not expose arbitrary host IO.
type Exchange func(context.Context, json.RawMessage) (json.RawMessage, error)

const maxExchangeMessage = 64 << 10
const maxExchangeTotal = 1 << 20
const maxExchangeCalls = 128

var exchangeMarker = []byte("\x1eTCSIM_CALL ")

type exchangeInput struct {
	context context.Context
	initial *bytes.Reader
	replies chan []byte
	pending []byte
}

func (in *exchangeInput) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if in.initial.Len() > 0 {
		return in.initial.Read(p)
	}
	if len(in.pending) == 0 {
		select {
		case <-in.context.Done():
			return 0, in.context.Err()
		case in.pending = <-in.replies:
		}
	}
	n := copy(p, in.pending)
	in.pending = in.pending[n:]
	return n, nil
}

type exchangeOutput struct {
	context      context.Context
	call         Exchange
	input        *exchangeInput
	output       *cappedOutput
	pending      []byte
	calls, total int
	err          error
}

func (out *exchangeOutput) Write(p []byte) (int, error) {
	if out.err != nil {
		return 0, out.err
	}
	for _, c := range p {
		out.pending = append(out.pending, c)
		if len(out.pending) > MaxOutputBytes || bytes.HasPrefix(out.pending, exchangeMarker) && len(out.pending) > maxExchangeMessage+len(exchangeMarker) && c != '\n' {
			out.err = errors.New("Capability message or output exceeds its limit")
			return 0, out.err
		}
		if c == '\n' {
			line := out.pending
			out.pending = nil
			if bytes.HasPrefix(line, exchangeMarker) {
				request := bytes.TrimSuffix(line[len(exchangeMarker):], []byte{'\n'})
				out.calls++
				out.total += len(request)
				if out.calls > maxExchangeCalls || out.total > maxExchangeTotal || !json.Valid(request) {
					out.err = errors.New("Invalid or excessive capability requests")
					return 0, out.err
				}
				response, err := out.call(out.context, request)
				if err != nil {
					response = json.RawMessage(`{"error":{"code":"CapabilityFailure","message":"Local capability request failed."}}`)
				}
				out.total += len(response)
				if len(response) > maxExchangeMessage || out.total > maxExchangeTotal || !json.Valid(response) {
					out.err = errors.New("Invalid or excessive capability response")
					return 0, out.err
				}
				// The guest must read its response before issuing another request. Never
				// block stdout on stdin: WASI write completes before Python starts read.
				response = append(append([]byte(nil), response...), '\n')
				select {
				case <-out.context.Done():
					out.err = out.context.Err()
				case out.input.replies <- response:
				default:
					out.err = errors.New("Capability response was not consumed")
				}
				if out.err != nil {
					return 0, out.err
				}
			} else if _, err := out.output.Write(line); err != nil {
				out.err = err
				return 0, err
			}
		}
	}
	return len(p), nil
}
func (out *exchangeOutput) finish() error {
	if out.err != nil {
		return out.err
	}
	if len(out.pending) > 0 {
		if bytes.HasPrefix(out.pending, exchangeMarker) {
			return errors.New("Incomplete capability request")
		}
		_, err := out.output.Write(out.pending)
		return err
	}
	return nil
}

var _ io.Reader = (*exchangeInput)(nil)
var _ io.Writer = (*exchangeOutput)(nil)
