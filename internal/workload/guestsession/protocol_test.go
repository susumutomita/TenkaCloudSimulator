package guestsession

import (
	"bytes"
	"encoding/binary"
	"testing"
)

func TestProtocolRoundTripAndReplay(t *testing.T) {
	var wire bytes.Buffer
	out := NewCodec(&wire)
	id := ID{1}
	size, _ := Size(24, 80)
	for _, f := range []Frame{{Hello, ID{}, []byte("01234567-89ab-cdef-0123-456789abcdef")}, {Start, id, size}, {Input, id, []byte("systemctl stop nginx\r")}} {
		if e := out.Write(f); e != nil {
			t.Fatal(e)
		}
	}
	original := append([]byte(nil), wire.Bytes()...)
	in := NewCodec(&wire)
	for _, kind := range []byte{Hello, Start, Input} {
		f, e := in.Read()
		if e != nil || f.Kind != kind {
			t.Fatalf("%v %v", f, e)
		}
	}
	wire.Write(original)
	if _, e := in.Read(); e == nil {
		t.Fatal("sequence replay accepted")
	}
}
func TestProtocolRejectsMalformedBoundsBeforeAllocation(t *testing.T) {
	for _, mutate := range []func([]byte){func(b []byte) { b[5] = 1 }, func(b []byte) { binary.BigEndian.PutUint32(b[32:], MaxPayload+1) }, func(b []byte) { binary.BigEndian.PutUint64(b[8:], 0) }, func(b []byte) { b[4] = 255 }} {
		var w bytes.Buffer
		if e := NewCodec(&w).Write(Frame{Ready, ID{1}, nil}); e != nil {
			t.Fatal(e)
		}
		b := append([]byte(nil), w.Bytes()...)
		mutate(b)
		if _, e := NewCodec(bytes.NewBuffer(b)).Read(); e == nil {
			t.Fatal("invalid frame accepted")
		}
	}
}
func TestProtocolRejectsUnknownIdentityAndSize(t *testing.T) {
	for _, f := range []Frame{{Ready, ID{}, nil}, {Hello, ID{}, []byte("not-a-vm")}, {Start, ID{1}, []byte{0, 0, 0, 80}}, {Input, ID{1}, make([]byte, MaxPayload+1)}} {
		var w bytes.Buffer
		if e := NewCodec(&w).Write(f); e == nil || w.Len() != 0 {
			t.Fatal("invalid write touched stream")
		}
	}
}
