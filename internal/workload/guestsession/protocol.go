// Package guestsession transports bounded interactive PTY bytes over an already
// retained private guest channel. It never dials, launches a host process, or
// treats QGA guest-exec as an interactive session.
package guestsession

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"regexp"
	"sync"
)

const MaxPayload = 8192
const headerBytes = 36
const (
	Hello byte = iota + 1
	Start
	Ready
	Input
	Output
	Resize
	Close
	Exit
	Failure
)

var ErrProtocol = errors.New("invalid guest session frame")
var uuidPattern = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

type ID [16]byte
type Frame struct {
	Kind    byte
	Session ID
	Payload []byte
}
type Codec struct {
	stream                      io.ReadWriter
	readMu, writeMu             sync.Mutex
	readSequence, writeSequence uint64
}

func NewCodec(stream io.ReadWriter) *Codec { return &Codec{stream: stream} }
func ValidUUID(s string) bool              { return uuidPattern.MatchString(s) }
func (f Frame) valid() bool {
	if len(f.Payload) > MaxPayload {
		return false
	}
	if f.Kind == Hello {
		return f.Session == ID{} && ValidUUID(string(f.Payload))
	}
	if f.Session == (ID{}) {
		return false
	}
	switch f.Kind {
	case Start, Resize:
		return len(f.Payload) == 4 && validSize(f.Payload)
	case Ready, Close:
		return len(f.Payload) == 0
	case Input, Output:
		return len(f.Payload) > 0
	case Exit:
		return len(f.Payload) == 4
	case Failure:
		return string(f.Payload) == "unavailable" || string(f.Payload) == "overflow" || string(f.Payload) == "protocol"
	}
	return false
}
func validSize(b []byte) bool {
	rows, cols := binary.BigEndian.Uint16(b[:2]), binary.BigEndian.Uint16(b[2:])
	return rows >= 1 && rows <= 300 && cols >= 1 && cols <= 500
}
func Size(rows, cols uint16) ([]byte, error) {
	b := make([]byte, 4)
	binary.BigEndian.PutUint16(b, rows)
	binary.BigEndian.PutUint16(b[2:], cols)
	if !validSize(b) {
		return nil, ErrProtocol
	}
	return b, nil
}
func (c *Codec) Read() (Frame, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	var f Frame
	var h [headerBytes]byte
	if c.stream == nil {
		return f, ErrProtocol
	}
	if _, err := io.ReadFull(c.stream, h[:]); err != nil {
		return f, err
	}
	if string(h[:4]) != "TCS1" || h[5] != 0 || h[6] != 0 || h[7] != 0 || c.readSequence == math.MaxUint64 || binary.BigEndian.Uint64(h[8:16]) != c.readSequence+1 {
		return f, ErrProtocol
	}
	n := binary.BigEndian.Uint32(h[32:])
	if n > MaxPayload {
		return f, ErrProtocol
	}
	f.Kind = h[4]
	copy(f.Session[:], h[16:32])
	f.Payload = make([]byte, int(n))
	if _, err := io.ReadFull(c.stream, f.Payload); err != nil {
		return Frame{}, err
	}
	if !f.valid() {
		return Frame{}, ErrProtocol
	}
	c.readSequence++
	return f, nil
}
func (c *Codec) Write(f Frame) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.stream == nil || !f.valid() || c.writeSequence == math.MaxUint64 {
		return ErrProtocol
	}
	b := make([]byte, headerBytes+len(f.Payload))
	copy(b, "TCS1")
	b[4] = f.Kind
	binary.BigEndian.PutUint64(b[8:16], c.writeSequence+1)
	copy(b[16:32], f.Session[:])
	binary.BigEndian.PutUint32(b[32:36], uint32(len(f.Payload)))
	copy(b[36:], f.Payload)
	for len(b) > 0 {
		n, err := c.stream.Write(b)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(b) {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	c.writeSequence++
	return nil
}
