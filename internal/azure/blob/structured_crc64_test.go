package blob

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"io"
	"testing"
)

func TestEncodeStructuredCRC64SliceGolden(t *testing.T) {
	for _, tc := range []struct {
		name string
		body []byte
		want string
	}{
		{"empty", nil, "012700000000000000010001000100000000000000000000000000000000000000000000000000"},
		// The documented two-segment example has these same two data bytes.
		// Its combined CRC is used here for both single-segment trailers.
		{"short-slice", []byte{0x11, 0x22}, "01290000000000000001000100010002000000000000001122e2a6377450adc2efe2a6377450adc2ef"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := encodeStructuredCRC64Slice(tc.body)
			if !ok || hex.EncodeToString(got) != tc.want {
				t.Fatalf("encoded wire = %x, ok = %v", got, ok)
			}
			decoded, valid := decodeStructuredCRC64TestSlice(got)
			if !valid || !bytes.Equal(decoded, tc.body) {
				t.Fatalf("independent decode = %x, valid = %v", decoded, valid)
			}
		})
	}
}

func TestEncodeStructuredCRC64SliceBoundsAndIntegrity(t *testing.T) {
	limit := make([]byte, 4<<20)
	wire, ok := encodeStructuredCRC64Slice(limit)
	if !ok || len(wire)-len(limit) != 39 {
		t.Fatalf("maximum wire length = %d, ok = %v", len(wire), ok)
	}
	if over, ok := encodeStructuredCRC64Slice(append(limit, 0)); ok || over != nil {
		t.Fatal("over-limit slice encoded")
	}

	payload := []byte{0x11, 0x22}
	wire, ok = encodeStructuredCRC64Slice(payload)
	if !ok {
		t.Fatal("short slice refused")
	}
	payload[0] ^= 1
	if wire[23] != 0x11 {
		t.Fatal("encoded body aliases input")
	}
	for _, off := range []int{0, 1, 9, 11, 13, 15, 23, 25, 33} {
		changed := bytes.Clone(wire)
		changed[off] ^= 1
		if _, valid := decodeStructuredCRC64TestSlice(changed); valid {
			t.Errorf("independent decoder accepted changed byte %d", off)
		}
	}
	if _, valid := decodeStructuredCRC64TestSlice(wire[:len(wire)-1]); valid {
		t.Error("independent decoder accepted truncation")
	}
}

// Test oracle uses sequential fields and a bitwise reflected CRC, not the
// encoder's offsets or Go's CRC64 table. Only tiny literal vectors use it.
func decodeStructuredCRC64TestSlice(wire []byte) ([]byte, bool) {
	r := bytes.NewReader(wire)
	read := func(field any) bool { return binary.Read(r, binary.LittleEndian, field) == nil }
	var version uint8
	var messageLength, segmentLength, segmentCRC, messageCRC uint64
	var flags, segments, segment uint16
	if !read(&version) || version != 1 || !read(&messageLength) || messageLength != uint64(len(wire)) ||
		!read(&flags) || flags != 1 || !read(&segments) || segments != 1 || !read(&segment) || segment != 1 ||
		!read(&segmentLength) || r.Len() < 16 || segmentLength != uint64(r.Len()-16) {
		return nil, false
	}
	data := make([]byte, int(segmentLength))
	if _, err := io.ReadFull(r, data); err != nil || !read(&segmentCRC) || !read(&messageCRC) || r.Len() != 0 {
		return nil, false
	}

	crc := ^uint64(0)
	for _, octet := range data {
		crc ^= uint64(octet)
		for bit := 0; bit < 8; bit++ {
			if crc&1 == 0 {
				crc >>= 1
			} else {
				crc = (crc >> 1) ^ uint64(0x9a6c9329ac4bc9b5)
			}
		}
	}
	computed := ^crc
	return data, computed == segmentCRC && computed == messageCRC
}
