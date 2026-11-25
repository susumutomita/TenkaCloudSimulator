package blob

import (
	"encoding/binary"
	"hash/crc64"
)

const structuredCRC64SegmentMax = 4 << 20

// encodeStructuredCRC64Slice packages one already-selected payload as an XSM
// 1.0 message. It does not select ranges, negotiate REST versions, emit HTTP
// headers, or alter persisted blob properties. A payload over one segment is
// unsupported and returns ok=false before allocating an encoded body.
func encodeStructuredCRC64Slice(payload []byte) (wire []byte, ok bool) {
	if len(payload) > structuredCRC64SegmentMax {
		return nil, false
	}

	const payloadOffset = 13 + 10
	const checksumBytes = 8
	wire = make([]byte, payloadOffset+len(payload)+2*checksumBytes)
	little := binary.LittleEndian
	wire[0] = 1
	little.PutUint64(wire[1:9], uint64(len(wire)))
	little.PutUint16(wire[9:11], 1)  // Include CRC64 in both trailers.
	little.PutUint16(wire[11:13], 1) // One segment.
	little.PutUint16(wire[13:15], 1) // Segment is numbered from one.
	little.PutUint64(wire[15:23], uint64(len(payload)))
	copy(wire[payloadOffset:], payload)

	// With one segment the segment and complete message cover identical data.
	digest := crc64.Checksum(payload, azureCRC64Table)
	trailer := payloadOffset + len(payload)
	little.PutUint64(wire[trailer:trailer+checksumBytes], digest)
	little.PutUint64(wire[trailer+checksumBytes:], digest)
	return wire, true
}
