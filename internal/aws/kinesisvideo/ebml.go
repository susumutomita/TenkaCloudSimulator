// Package kinesisvideo handles the finite Matroska media profile used by
// the local Kinesis Video Streams data plane.
package kinesisvideo

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math/bits"
)

const (
	ebmlHeader = 0x1a45dfa3
	segmentID  = 0x18538067
	infoID     = 0x1549a966
	tracksID   = 0x1654ae6b
	clusterID  = 0x1f43b675
	tagsID     = 0x1254c367
)

type element struct {
	id   uint32
	data []byte
	wire []byte
}

// vint reads an EBML variable-size integer (RFC 8794 section 4). An element
// identifier keeps its width marker, while a size or track number drops it.
func vint(data []byte, maxWidth int, keepMarker bool) (uint64, int, error) {
	if len(data) == 0 || data[0] == 0 {
		return 0, 0, fmt.Errorf("missing EBML integer")
	}
	width := bits.LeadingZeros8(data[0]) + 1
	if width > maxWidth || width > len(data) {
		return 0, 0, fmt.Errorf("invalid EBML integer width")
	}
	value := uint64(data[0])
	if !keepMarker {
		value &^= 1 << (8 - width)
	}
	for _, b := range data[1:width] {
		value = value<<8 | uint64(b)
	}
	return value, width, nil
}

// children returns views into the bounded input. Only the outer Segment may
// omit its size, since the complete finite upload provides its boundary.
func children(data []byte, outer bool) ([]element, error) {
	var result []element
	var checksum *element
	for cursor := data; len(cursor) > 0; {
		if len(result) == 2048 {
			return nil, fmt.Errorf("too many Matroska elements")
		}
		id, idWidth, err := vint(cursor, 4, true)
		if err != nil {
			return nil, err
		}
		size, sizeWidth, err := vint(cursor[idWidth:], 8, false)
		if err != nil {
			return nil, err
		}
		header := idWidth + sizeWidth
		if size == (uint64(1)<<(7*sizeWidth))-1 {
			if !outer || id != segmentID {
				return nil, fmt.Errorf("unknown size on a nested Matroska element")
			}
			size = uint64(len(cursor) - header)
		}
		if size > uint64(len(cursor)-header) {
			return nil, fmt.Errorf("truncated Matroska element")
		}
		end := header + int(size)
		item := element{uint32(id), cursor[header:end], cursor[:end]}
		if item.id == 0xbf {
			if outer || checksum != nil || len(item.data) != 4 || len(result) != 0 {
				return nil, fmt.Errorf("invalid Matroska CRC-32 element")
			}
			checksum = &item
			if binary.LittleEndian.Uint32(item.data) != crc32.ChecksumIEEE(cursor[end:]) {
				return nil, fmt.Errorf("Matroska CRC-32 mismatch")
			}
		}
		result = append(result, item)
		cursor = cursor[end:]
	}
	return result, nil
}

func one(items []element, id uint32) (element, error) {
	var found element
	for _, item := range items {
		if item.id == id {
			if found.wire != nil {
				return element{}, fmt.Errorf("duplicate Matroska element %x", id)
			}
			found = item
		}
	}
	if found.wire == nil {
		return element{}, fmt.Errorf("missing Matroska element %x", id)
	}
	return found, nil
}

func uintValue(item element) (uint64, error) {
	if len(item.data) == 0 || len(item.data) > 8 {
		return 0, fmt.Errorf("invalid Matroska unsigned integer")
	}
	var result uint64
	for _, b := range item.data {
		result = result<<8 | uint64(b)
	}
	return result, nil
}

func uintField(items []element, id uint32) (uint64, error) {
	item, err := one(items, id)
	if err != nil {
		return 0, err
	}
	return uintValue(item)
}

func appendElement(dst []byte, id uint32, payload []byte) []byte {
	var buf [8]byte
	binary.BigEndian.PutUint32(buf[:4], id)
	idWidth := (bits.Len32(id) + 7) / 8
	dst = append(dst, buf[4-idWidth:4]...)
	size := uint64(len(payload))
	width := 1
	for size >= (uint64(1)<<(width*7))-1 {
		width++
	}
	binary.BigEndian.PutUint64(buf[:], size|(uint64(1)<<(width*7)))
	dst = append(dst, buf[8-width:]...)
	return append(dst, payload...)
}
