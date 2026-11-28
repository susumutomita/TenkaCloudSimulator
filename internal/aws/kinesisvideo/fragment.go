package kinesisvideo

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
)

// MaxFragmentBytes bounds a single local, finite upload before parsing.
const MaxFragmentBytes = 4 << 20

// Fragment retains the actual encoded media. Its byte slices refer to the input
// to ParseFragment, which must remain immutable for the lifetime of the value.
type Fragment struct {
	header, info, tracks, cluster []byte
	FirstFrameMilliseconds        int64
	Frames                        int
}

// ParseFragment accepts a single Matroska segment and cluster with one AVC
// video track whose first marked keyframe contains an IDR NAL unit. It checks
// AVC framing and preserves the encoded bytes. Codec decoding is left to the
// consumer. Container metadata uses millisecond ticks.
func ParseFragment(data []byte) (Fragment, error) {
	if len(data) == 0 || len(data) > MaxFragmentBytes {
		return Fragment{}, fmt.Errorf("fragment size must be between 1 and %d bytes", MaxFragmentBytes)
	}
	top, err := children(data, true)
	if err != nil {
		return Fragment{}, err
	}
	if len(top) != 2 || top[0].id != ebmlHeader || top[1].id != segmentID {
		return Fragment{}, fmt.Errorf("expected a Matroska header and one segment")
	}
	header, err := children(top[0].data, false)
	if err != nil {
		return Fragment{}, err
	}
	doc, err := one(header, 0x4282)
	if err != nil || string(doc.data) != "matroska" {
		return Fragment{}, fmt.Errorf("expected Matroska DocType")
	}
	parts, err := children(top[1].data, false)
	if err != nil {
		return Fragment{}, err
	}
	for _, p := range parts {
		switch p.id {
		case infoID, tracksID, clusterID, tagsID, 0x114d9b74, 0x1c53bb6b, 0xec, 0xbf: // SeekHead, Cues, Void, CRC-32
		default:
			return Fragment{}, fmt.Errorf("unsupported Matroska segment element %x", p.id)
		}
	}
	info, err := one(parts, infoID)
	if err != nil {
		return Fragment{}, err
	}
	fields, err := children(info.data, false)
	if err != nil {
		return Fragment{}, err
	}
	// One millisecond is the Matroska default TimestampScale.
	var hasScale bool
	for _, f := range fields {
		switch f.id {
		case 0x2ad7b1:
			scale, err := uintValue(f)
			if err != nil || hasScale || scale != 1_000_000 {
				return Fragment{}, fmt.Errorf("expected a millisecond TimestampScale")
			}
			hasScale = true
		case 0x4d80, 0x5741, 0x4489, 0x4461, 0x7ba9, 0x73a4, 0xbf, 0xec:
			// MuxingApp, WritingApp, Duration, DateUTC, Title, SegmentUID, CRC and Void.
		default:
			return Fragment{}, fmt.Errorf("unsupported or linked segment information %x", f.id)
		}
	}
	tracks, err := one(parts, tracksID)
	if err != nil {
		return Fragment{}, err
	}
	number, nalWidth, err := readTrack(tracks.data)
	if err != nil {
		return Fragment{}, err
	}
	cluster, err := one(parts, clusterID)
	if err != nil {
		return Fragment{}, err
	}
	first, frames, err := readCluster(cluster.data, number, nalWidth)
	if err != nil {
		return Fragment{}, err
	}
	return Fragment{top[0].wire, info.wire, tracks.wire, cluster.wire, first, frames}, nil
}

func readTrack(data []byte) (uint64, int, error) {
	items, err := children(data, false)
	if err != nil {
		return 0, 0, err
	}
	for _, item := range items {
		if item.id != 0xae && item.id != 0xbf && item.id != 0xec {
			return 0, 0, fmt.Errorf("unsupported entry in the track table")
		}
	}
	entry, err := one(items, 0xae)
	if err != nil {
		return 0, 0, err
	}
	items, err = children(entry.data, false)
	if err != nil {
		return 0, 0, err
	}
	for _, item := range items {
		switch item.id {
		case 0xd7, 0x73c5, 0x83, 0x86, 0x63a2, 0xe0, 0x9c, 0x23e383,
			0x55ee, 0x22b59c, 0x22b59d, 0x88, 0xb9, 0x55aa, 0x536e, 0x258688, 0xbf, 0xec:
		default:
			return 0, 0, fmt.Errorf("unsupported video track setting %x", item.id)
		}
	}
	if uid, err := uintField(items, 0x73c5); err != nil || uid == 0 {
		return 0, 0, fmt.Errorf("invalid track UID")
	}
	kind, err := uintField(items, 0x83)
	if err != nil || kind != 1 {
		return 0, 0, fmt.Errorf("expected a video track")
	}
	number, err := uintField(items, 0xd7)
	if err != nil || number == 0 || number > math.MaxInt32 {
		return 0, 0, fmt.Errorf("invalid video track number")
	}
	codec, err := one(items, 0x86)
	if err != nil || string(codec.data) != "V_MPEG4/ISO/AVC" {
		return 0, 0, fmt.Errorf("expected an AVC video track")
	}
	video, err := one(items, 0xe0)
	if err != nil {
		return 0, 0, err
	}
	dim, err := children(video.data, false)
	if err != nil {
		return 0, 0, err
	}
	for _, id := range []uint32{0xb0, 0xba} {
		value, err := uintField(dim, id)
		if err != nil || value == 0 || value > 4096 {
			return 0, 0, fmt.Errorf("invalid bounded video dimensions")
		}
	}
	config, err := one(items, 0x63a2)
	if err != nil {
		return 0, 0, err
	}
	width, err := avcLengthWidth(config.data)
	return number, width, err
}

// avcLengthWidth checks the AVCDecoderConfigurationRecord and both parameter
// set lists. This profile uses the baseline layout without trailing extensions.
func avcLengthWidth(data []byte) (int, error) {
	if len(data) < 7 || data[0] != 1 || data[4]&0xfc != 0xfc || data[5]&0xe0 != 0xe0 {
		return 0, fmt.Errorf("invalid AVC codec-private record")
	}
	width := int(data[4]&3) + 1
	if width == 3 {
		return 0, fmt.Errorf("reserved AVC NAL length width")
	}
	count := int(data[5] & 31)
	rest := data[6:]
	for _, kind := range []byte{7, 8} {
		if kind == 8 {
			if len(rest) == 0 {
				return 0, fmt.Errorf("missing AVC picture parameter sets")
			}
			count, rest = int(rest[0]), rest[1:]
		}
		if count == 0 {
			return 0, fmt.Errorf("empty AVC parameter set list")
		}
		for range count {
			if len(rest) < 2 {
				return 0, fmt.Errorf("truncated AVC parameter set")
			}
			length := int(binary.BigEndian.Uint16(rest))
			if length < 2 || length > len(rest)-2 || rest[2]&0x9f != kind {
				return 0, fmt.Errorf("invalid AVC parameter set")
			}
			rest = rest[2+length:]
		}
	}
	if len(rest) != 0 {
		return 0, fmt.Errorf("unsupported trailing AVC codec-private data")
	}
	return width, nil
}

func readCluster(data []byte, track uint64, nalWidth int) (int64, int, error) {
	items, err := children(data, false)
	if err != nil {
		return 0, 0, err
	}
	start, err := uintField(items, 0xe7)
	if err != nil || start > math.MaxInt64-math.MaxInt16 {
		return 0, 0, fmt.Errorf("invalid cluster timestamp")
	}
	var frames int
	var first, previous int64
	for _, item := range items {
		switch item.id {
		case 0xe7, 0xbf, 0xec: // Timestamp, CRC-32, Void
			continue
		case 0xa3: // SimpleBlock
		default:
			return 0, 0, fmt.Errorf("unsupported Matroska cluster element %x", item.id)
		}
		number, n, err := vint(item.data, 8, false)
		if err != nil || number != track || len(item.data) <= n+3 {
			return 0, 0, fmt.Errorf("invalid video block")
		}
		flags := item.data[n+2]
		if flags&0x7e != 0 {
			return 0, 0, fmt.Errorf("expected visible, unlaced video block")
		}
		stamp := int64(start) + int64(int16(binary.BigEndian.Uint16(item.data[n:])))
		idr, err := checkNALs(item.data[n+3:], nalWidth)
		if err != nil {
			return 0, 0, err
		}
		if frames == 0 {
			if !idr || flags&0x80 == 0 || stamp < 0 {
				return 0, 0, fmt.Errorf("first frame must be marked as a keyframe and contain an AVC IDR NAL")
			}
			first = stamp
		}
		if stamp < previous || stamp-first > 10_000 || frames == 512 {
			return 0, 0, fmt.Errorf("video exceeds the local fragment time or frame bound")
		}
		frames++
		previous = stamp
	}
	if frames == 0 {
		return 0, 0, fmt.Errorf("empty video cluster")
	}
	return first, frames, nil
}

func checkNALs(data []byte, width int) (bool, error) {
	var idr, hasPicture bool
	for count := 0; len(data) > 0; count++ {
		if len(data) < width || count == 256 {
			return false, fmt.Errorf("invalid AVC NAL count or length")
		}
		var size uint64
		for _, b := range data[:width] {
			size = size<<8 | uint64(b)
		}
		data = data[width:]
		if size == 0 || size > uint64(len(data)) || data[0]&0x80 != 0 {
			return false, fmt.Errorf("invalid AVC NAL data")
		}
		kind := data[0] & 31
		if (kind == 1 || kind == 5) && size < 2 {
			return false, fmt.Errorf("AVC picture NAL requires a slice payload")
		}
		idr = idr || kind == 5
		hasPicture = hasPicture || kind == 1 || kind == 5
		data = data[int(size):]
	}
	if !hasPicture {
		return false, fmt.Errorf("AVC block contains no picture")
	}
	return idr, nil
}

// MediaChunk returns an independent MKV segment for a stored fragment. Each
// AWS tag comes from the owning service's stored receipt and cursor. Producer
// tags, seek offsets and cues from the uploaded file cannot describe this new
// segment; its immutable encoded track and cluster are copied byte for byte.
func (f Fragment) MediaChunk(number, producerTimestamp, serverTimestamp, continuation, millisBehind string) ([]byte, error) {
	values := []struct{ key, value string }{
		{"FRAGMENT_NUMBER", number}, {"PRODUCER_TIMESTAMP", producerTimestamp},
		{"SERVER_TIMESTAMP", serverTimestamp}, {"CONTINUATION_TOKEN", continuation},
		{"MILLIS_BEHIND_NOW", millisBehind},
	}
	if len(f.header) == 0 || f.Frames == 0 {
		return nil, fmt.Errorf("invalid parsed fragment")
	}
	var tag []byte
	tag = appendElement(tag, 0x63c0, nil) // Targets an entire segment.
	for _, v := range values {
		if len(v.value) == 0 || len(v.value) > 4096 {
			return nil, fmt.Errorf("invalid fragment receipt")
		}
		simple := appendElement(nil, 0x45a3, []byte("AWS_KINESISVIDEO_"+v.key))
		simple = appendElement(simple, 0x4487, []byte(v.value))
		tag = appendElement(tag, 0x67c8, simple)
	}
	tags := appendElement(nil, tagsID, appendElement(nil, 0x7373, tag))
	segment := bytes.Join([][]byte{f.info, f.tracks, tags, f.cluster}, nil)
	return appendElement(bytes.Clone(f.header), segmentID, segment), nil
}
