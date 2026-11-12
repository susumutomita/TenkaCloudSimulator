// Package guestnet implements bounded wire primitives for a private guest
// Ethernet transport. It does not open sockets, launch guests, authorize packets,
// or provide IP/TCP networking. No production VM currently uses this package.
package guestnet

import (
	"encoding/binary"
	"errors"
	"io"
	"sync"
)

// Frames contain Ethernet headers and payload, without FCS or a virtio header.
// The prospective device uses MTU 1500 with offload disabled. VLANs, jumbo frames
// and GSO require a separate reviewed contract; this codec does not authorize them.
const (
	MinFrameBytes = 14
	MaxFrameBytes = 1514
)

var (
	ErrFrameSize    = errors.New("guest Ethernet frame length out of bounds")
	ErrStreamFailed = errors.New("guest Ethernet stream is unusable")
)

// Decoder reads QEMU socket-netdev framing: a four-byte big-endian unsigned
// length followed by exactly that many Ethernet bytes. Calls are serialized.
// After any incomplete read or invalid length the decoder is unusable: attempting
// to resynchronize could treat attacker-controlled body bytes as a new header.
// The owner must impose deadlines and close the underlying stream on revocation;
// this codec cannot interrupt an arbitrary blocked io.Reader.
type Decoder struct {
	mu     sync.Mutex
	source io.Reader
	failed bool
}

func NewDecoder(source io.Reader) *Decoder { return &Decoder{source: source} }

func (d *Decoder) ReadFrame() ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failed || d.source == nil {
		d.failed = true
		return nil, ErrStreamFailed
	}
	var header [4]byte
	if _, err := io.ReadFull(d.source, header[:]); err != nil {
		d.failed = true
		return nil, err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size < MinFrameBytes || size > MaxFrameBytes {
		d.failed = true
		return nil, ErrFrameSize
	}
	frame := make([]byte, int(size))
	if _, err := io.ReadFull(d.source, frame); err != nil {
		d.failed = true
		return nil, err
	}
	return frame, nil
}

// Encoder writes the same bounded framing. Calls are serialized to prevent
// concurrent writers interleaving frame headers and payloads. Any write failure
// permanently poisons the stream because delivery may have been partial. Invalid
// local frame lengths do not touch the stream. Callers must not mutate a frame
// concurrently with WriteFrame. The owner supplies deadlines and revocation.
type Encoder struct {
	mu          sync.Mutex
	destination io.Writer
	failed      bool
}

func NewEncoder(destination io.Writer) *Encoder { return &Encoder{destination: destination} }

func (e *Encoder) WriteFrame(frame []byte) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.failed || e.destination == nil {
		e.failed = true
		return ErrStreamFailed
	}
	if len(frame) < MinFrameBytes || len(frame) > MaxFrameBytes {
		return ErrFrameSize
	}
	// One fixed-size buffer limits allocation independent of untrusted input and
	// ensures the length and body are submitted as one logical record.
	var packet [4 + MaxFrameBytes]byte
	binary.BigEndian.PutUint32(packet[:4], uint32(len(frame)))
	copy(packet[4:], frame)
	data := packet[:4+len(frame)]
	n, err := e.destination.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		e.failed = true
	}
	return err
}
