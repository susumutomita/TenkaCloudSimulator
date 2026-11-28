package kinesisvideo

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"os"
	"strings"
	"testing"
)

// ffmpeg generated these two distinct 32x24 source frames locally. The
// fixture contains AVC configuration, two IDR pictures and normal MKV CRCs.
func clip(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/two-frames.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(data)); got != "8641f34746c64fdd20c72c6374f2d6b86e179fe76ac22d69c2c3dbd3038fdd9f" {
		t.Fatal(got)
	}
	return data
}

func TestStoredMediaChunk(t *testing.T) {
	source := clip(t)
	f, err := ParseFragment(source)
	if err != nil || f.Frames != 2 || f.FirstFrameMilliseconds != 0 {
		t.Fatalf("fragment frames=%d start=%d err=%v", f.Frames, f.FirstFrameMilliseconds, err)
	}
	chunk, err := f.MediaChunk("912", "11.125", "12.250", "saved-fragment-two", "17")
	if err != nil {
		t.Fatal(err)
	}
	retrieved, err := ParseFragment(chunk)
	if err != nil || retrieved.Frames != f.Frames || !bytes.Equal(f.tracks, retrieved.tracks) || !bytes.Equal(f.cluster, retrieved.cluster) {
		t.Fatalf("retrieved media differs: %v", err)
	}
	top, _ := children(chunk, true)
	parts, _ := children(top[1].data, false)
	tags, err := one(parts, tagsID)
	if err != nil {
		t.Fatal(err)
	}
	sets, _ := children(tags.data, false)
	group, err := one(sets, 0x7373)
	if err != nil {
		t.Fatal(err)
	}
	fields, _ := children(group.data, false)
	want := map[string]string{"FRAGMENT_NUMBER": "912", "PRODUCER_TIMESTAMP": "11.125", "SERVER_TIMESTAMP": "12.250", "CONTINUATION_TOKEN": "saved-fragment-two", "MILLIS_BEHIND_NOW": "17"}
	for _, field := range fields {
		if field.id == 0x63c0 {
			continue
		}
		entries, err := children(field.data, false)
		if field.id != 0x67c8 || err != nil {
			t.Fatal(field.id, err)
		}
		key, _ := one(entries, 0x45a3)
		value, _ := one(entries, 0x4487)
		name, ok := strings.CutPrefix(string(key.data), "AWS_KINESISVIDEO_")
		if !ok || want[name] != string(value.data) {
			t.Fatalf("unexpected tag %q: %q", key.data, value.data)
		}
		delete(want, name)
	}
	if len(want) != 0 {
		t.Fatal("missing tags", want)
	}
	// Replace an old retrieved segment's tags with the new trusted receipt.
	newChunk, err := retrieved.MediaChunk("913", "13.000", "14.000", "next-cursor", "0")
	if err != nil || bytes.Contains(newChunk, []byte("saved-fragment-two")) || !bytes.Contains(newChunk, []byte("next-cursor")) {
		t.Fatal("prior fragment tags survived replacement", err)
	}
}

func TestRejectInvalidFragment(t *testing.T) {
	source := clip(t)
	original, err := ParseFragment(source)
	if err != nil {
		t.Fatal(err)
	}
	damaged := bytes.Clone(source)
	idx := bytes.Index(damaged, original.cluster)
	damaged[idx+len(original.cluster)-1] ^= 1
	dupCluster := appendElement(nil, segmentID, bytes.Join([][]byte{original.info, original.tracks, original.cluster, original.cluster}, nil))
	badType := bytes.Replace(source, []byte("matroska"), []byte("notvideo"), 1)
	for name, input := range map[string][]byte{
		"empty": nil, "truncated": source[:len(source)-2], "CRC mismatch": damaged,
		"wrong type": badType, "oversize": make([]byte, MaxFragmentBytes+1),
		"two clusters": append(bytes.Clone(original.header), dupCluster...),
		"two segments": append(bytes.Clone(source), source...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseFragment(input); err == nil {
				t.Fatal("accepted invalid fragment")
			}
		})
	}
	// Wrong track, unattested first key picture, laced blocks, truncated NALs,
	// reversed timestamps and missing/empty cluster are refused independently.
	cluster, err := children(original.cluster, false)
	if err != nil || len(cluster) != 1 {
		t.Fatal("invalid fixture cluster", err)
	}
	blocks, err := children(cluster[0].data, false)
	if err != nil {
		t.Fatal(err)
	}
	var good [][]byte
	for _, b := range blocks {
		if b.id == 0xa3 {
			good = append(good, b.data)
		}
	}
	if len(good) != 2 {
		t.Fatal("fixture is missing its two frames")
	}
	for _, test := range []struct {
		name   string
		change func([]byte)
	}{
		{"other track", func(b []byte) { b[0] = 0x82 }}, {"not a keyframe", func(b []byte) { b[3] &^= 0x80 }},
		{"laced", func(b []byte) { b[3] |= 0x02 }}, {"out of range length", func(b []byte) { b[4] = 0x7f }},
	} {
		t.Run(test.name, func(t *testing.T) {
			b := bytes.Clone(good[0])
			test.change(b)
			data := appendElement(nil, 0xe7, []byte{0})
			data = appendElement(data, 0xa3, b)
			if _, _, err := readCluster(data, 1, 4); err == nil {
				t.Fatal("accepted invalid block")
			}
		})
	}
	late := bytes.Clone(good[0])
	binary.BigEndian.PutUint16(late[1:3], 30)
	early := bytes.Clone(good[1])
	binary.BigEndian.PutUint16(early[1:3], 20)
	data := appendElement(nil, 0xe7, []byte{0})
	data = appendElement(data, 0xa3, late)
	data = appendElement(data, 0xa3, early)
	if _, _, err := readCluster(data, 1, 4); err == nil {
		t.Fatal("accepted reversed relative timestamps")
	}
}

func TestAVCParameterSetsAndFrames(t *testing.T) {
	good := []byte{1, 0x42, 0xc0, 10, 0xff, 0xe1, 0, 2, 0x67, 0x42, 1, 0, 2, 0x68, 0xce}
	for _, width := range []int{1, 2, 4} {
		config := bytes.Clone(good)
		config[4] = 0xfc | byte(width-1)
		if got, err := avcLengthWidth(config); err != nil || got != width {
			t.Fatalf("NAL width %d: got %d, %v", width, got, err)
		}
	}
	for _, test := range []struct {
		index int
		value byte
	}{
		{0, 2}, {4, 0xfe}, {5, 0xe0}, {6, 0xff}, {8, 0x68}, {10, 0}, {12, 0xff}, {13, 0x67},
	} {
		config := bytes.Clone(good)
		config[test.index] = test.value
		if _, err := avcLengthWidth(config); err == nil {
			t.Fatalf("accepted invalid configuration byte %d", test.index)
		}
	}
	if _, err := avcLengthWidth(append(bytes.Clone(good), 0)); err == nil {
		t.Fatal("accepted trailing configuration")
	}
	for _, length := range []int{0, 6, 7, 9, 10, 11, 12, 14} {
		if _, err := avcLengthWidth(good[:length]); err == nil {
			t.Fatalf("accepted %d-byte record", length)
		}
	}
	for _, tt := range []struct {
		data []byte
		idr  bool
	}{
		{[]byte{2, 0x65, 0x10}, true}, {[]byte{2, 0x41, 0x10}, false},
		{[]byte{2, 0x06, 0x01, 2, 0x65, 0x10}, true},
	} {
		got, err := checkNALs(tt.data, 1)
		if err != nil || got != tt.idr {
			t.Fatalf("NAL check %x: %v, %v", tt.data, got, err)
		}
	}
	for _, b := range [][]byte{{}, {0}, {2, 0x65}, {1, 0xff}, {1, 0x65}, {1, 0x41}, {1, 0x06}, bytes.Repeat([]byte{2, 0x41, 0x10}, 257)} {
		if _, err := checkNALs(b, 1); err == nil {
			t.Fatal("accepted unsupported NAL payload")
		}
	}
}

func TestEBMLBoundaries(t *testing.T) {
	for _, size := range []int{0, 1, 126, 127, 128, 16382, 16383, 16384} {
		payload := bytes.Repeat([]byte{0xab}, size)
		wire := appendElement(nil, 0x4487, payload)
		items, err := children(wire, false)
		if err != nil || len(items) != 1 || items[0].id != 0x4487 || !bytes.Equal(items[0].data, payload) {
			t.Fatalf("size %d: %v", size, err)
		}
	}
	for _, data := range [][]byte{{0}, {1, 2}, {0xa3}, {0xa3, 0xff}, {0xa3, 0x84, 1, 2, 3}, bytes.Repeat([]byte{0xec, 0x80}, 2049)} {
		if _, err := children(data, false); err == nil {
			t.Fatal("accepted invalid element sequence")
		}
	}
	source := clip(t)
	items, _ := children(source, true)
	unknown := append(bytes.Clone(items[0].wire), 0x18, 0x53, 0x80, 0x67, 0xff)
	unknown = append(unknown, items[1].data...)
	if f, err := ParseFragment(unknown); err != nil || f.Frames != 2 {
		t.Fatal("finite upload with unknown Segment size", err)
	}
	if _, err := (Fragment{}).MediaChunk("1", "2", "3", "next", "0"); err == nil {
		t.Fatal("empty fragment")
	}
	f, err := ParseFragment(source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.MediaChunk("1", "2", "3", "", "0"); err == nil {
		t.Fatal("missing continuation receipt")
	}
	root, err := children(f.cluster, false)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := children(root[0].data, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, relativeOffset := range []uint32{0xa7, 0xab} { // Position and PrevSize would be stale after repacking.
		data := appendElement(nil, relativeOffset, []byte{1})
		for _, item := range contents {
			if item.id != 0xbf {
				data = append(data, item.wire...)
			}
		}
		if _, _, err := readCluster(data, 1, 4); err == nil {
			t.Fatalf("accepted a position-dependent cluster field %x", relativeOffset)
		}
	}
}
