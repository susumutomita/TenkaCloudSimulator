package guestnet

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"sync"
	"testing"
	"testing/iotest"
)

func wire(frame []byte) []byte {
	b := make([]byte, 4+len(frame))
	binary.BigEndian.PutUint32(b, uint32(len(frame)))
	copy(b[4:], frame)
	return b
}

func TestFrameWireAndFragmentation(t *testing.T) {
	for _, size := range []int{14, 42, 60, 1500, 1514} {
		frame := bytes.Repeat([]byte{0xa5}, size)
		var out bytes.Buffer
		if err := NewEncoder(&out).WriteFrame(frame); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(out.Bytes(), wire(frame)) {
			t.Fatal("wrong wire bytes")
		}
		for _, r := range []io.Reader{bytes.NewReader(out.Bytes()), iotest.OneByteReader(bytes.NewReader(out.Bytes())), iotest.DataErrReader(bytes.NewReader(out.Bytes()))} {
			got, err := NewDecoder(r).ReadFrame()
			if err != nil || !bytes.Equal(got, frame) {
				t.Fatalf("size %d: %x %v", size, got, err)
			}
		}
	}
}

func TestDecoderPoisonAndBounds(t *testing.T) {
	for _, size := range []uint32{0, 1, 13, 1515, 65536, 0xffffffff} {
		header := make([]byte, 4)
		binary.BigEndian.PutUint32(header, size)
		r := bytes.NewReader(append(header, wire(make([]byte, 14))...))
		d := NewDecoder(r)
		if got, err := d.ReadFrame(); got != nil || !errors.Is(err, ErrFrameSize) {
			t.Fatalf("size %d: %v", size, err)
		}
		if r.Len() != 18 {
			t.Fatal("read body for invalid length")
		}
		if _, err := d.ReadFrame(); !errors.Is(err, ErrStreamFailed) {
			t.Fatal(err)
		}
		if r.Len() != 18 {
			t.Fatal("resynchronized rejected stream")
		}
	}
	full := wire(make([]byte, 14))
	for n := 0; n < len(full); n++ {
		d := NewDecoder(bytes.NewReader(full[:n]))
		if got, err := d.ReadFrame(); got != nil || err == nil {
			t.Fatalf("partial %d accepted", n)
		}
		if _, err := d.ReadFrame(); !errors.Is(err, ErrStreamFailed) {
			t.Fatal(err)
		}
	}
	for _, d := range []*Decoder{{}, NewDecoder(nil)} {
		if _, err := d.ReadFrame(); !errors.Is(err, ErrStreamFailed) {
			t.Fatal(err)
		}
	}
}

type brokenWriter struct {
	calls int
	n     int
	err   error
}

func (w *brokenWriter) Write(p []byte) (int, error) { w.calls++; return w.n, w.err }

func TestEncoderFailure(t *testing.T) {
	boom := errors.New("transport closed")
	for _, w := range []*brokenWriter{{n: 0}, {n: 3}, {n: 17}, {n: -1}, {n: 19}, {n: 18, err: boom}, {n: 3, err: boom}} {
		e := NewEncoder(w)
		if err := e.WriteFrame(make([]byte, 14)); err == nil {
			t.Fatal("accepted failed write")
		}
		if err := e.WriteFrame(make([]byte, 14)); !errors.Is(err, ErrStreamFailed) {
			t.Fatal(err)
		}
		if w.calls != 1 {
			t.Fatal("retried uncertain write")
		}
	}
	for _, e := range []*Encoder{{}, NewEncoder(nil)} {
		if err := e.WriteFrame(make([]byte, 14)); !errors.Is(err, ErrStreamFailed) {
			t.Fatal(err)
		}
	}
	var b bytes.Buffer
	e := NewEncoder(&b)
	for _, size := range []int{0, 13, 1515} {
		if err := e.WriteFrame(make([]byte, size)); !errors.Is(err, ErrFrameSize) {
			t.Fatal(err)
		}
		if b.Len() != 0 {
			t.Fatal("invalid frame reached transport")
		}
	}
	if err := e.WriteFrame(make([]byte, 14)); err != nil {
		t.Fatal("local validation poisoned stream", err)
	}
}

func TestConcurrentWholeFrames(t *testing.T) {
	var b bytes.Buffer
	e := NewEncoder(&b)
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := e.WriteFrame(bytes.Repeat([]byte{byte(i)}, 14)); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	d := NewDecoder(&b)
	var mu sync.Mutex
	seen := map[byte]bool{}
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			frame, err := d.ReadFrame()
			if err != nil {
				t.Error(err)
				return
			}
			if !bytes.Equal(frame, bytes.Repeat(frame[:1], 14)) {
				t.Error("interleaved frame")
			}
			mu.Lock()
			defer mu.Unlock()
			if seen[frame[0]] {
				t.Error("duplicate frame")
			}
			seen[frame[0]] = true
		}()
	}
	wg.Wait()
	if len(seen) != 64 || b.Len() != 0 {
		t.Fatal("lost frames")
	}
}

func FuzzDecodeFrame(f *testing.F) {
	for _, b := range [][]byte{nil, {0xff, 0xff, 0xff, 0xff}, wire(make([]byte, 14)), wire(make([]byte, 1514))} {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		d := NewDecoder(bytes.NewReader(b))
		got, err := d.ReadFrame()
		if err != nil {
			if got != nil {
				t.Fatal("partial frame exposed")
			}
			if _, next := d.ReadFrame(); !errors.Is(next, ErrStreamFailed) {
				t.Fatal("failed decoder resumed")
			}
			return
		}
		if len(got) < MinFrameBytes || len(got) > MaxFrameBytes {
			t.Fatal("unbounded frame")
		}
		if !bytes.Equal(wire(got), b[:4+len(got)]) {
			t.Fatal("wire mismatch")
		}
	})
}
