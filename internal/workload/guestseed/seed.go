// Package guestseed builds bounded NoCloud initialization media in memory.
// It neither executes UserData nor grants a guest host authority. A trusted
// lifecycle owner must bind each image to one lease and mount it read-only.
package guestseed

import (
	"encoding/binary"
	"errors"
	"fmt"
	"regexp"
)

const sector = 2048
const MaxUserDataBytes = 16 << 10

var identifier = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
var hostname = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Build creates an ISO9660 image with the cidata volume label and Rock Ridge
// names meta-data and user-data. instanceID must identify a fresh trusted guest
// incarnation, not a participant-chosen or reused cloud resource name. hostName
// is one DNS label. Metadata is generated; UserData bytes are copied verbatim.
// The image contains no credentials, network configuration or offline packages
// unless the trusted caller explicitly supplies them within UserData itself.
// Output is deterministic and private to the caller; this function writes no files.
func Build(instanceID, hostName string, userData []byte) ([]byte, error) {
	return build(instanceID, hostName, userData, nil)
}

func build(instanceID, hostName string, userData, network []byte) ([]byte, error) {
	if !identifier.MatchString(instanceID) || !hostname.MatchString(hostName) {
		return nil, errors.New("invalid guest seed identity or hostname")
	}
	if len(userData) == 0 || len(userData) > MaxUserDataBytes {
		return nil, errors.New("guest seed user data must contain 1 to 16384 bytes")
	}
	// Quote identity values to avoid YAML implicit scalar typing (e.g. true, null).
	metadata := []byte(fmt.Sprintf("instance-id: %q\nlocal-hostname: %q\n", instanceID, hostName))
	const rootBlock = 20
	const metaBlock = 21
	userBlock := metaBlock + blocks(len(metadata))
	networkBlock := userBlock + blocks(len(userData))
	volumeBlocks := networkBlock + blocks(len(network))
	image := make([]byte, volumeBlocks*sector)
	// Primary descriptor, terminator, little- and big-endian path tables.
	pvd := image[16*sector : 17*sector]
	descriptor(pvd, 1)
	padded(pvd[8:40], "TENKACLOUD")
	padded(pvd[40:72], "cidata")
	both32(pvd[80:88], uint32(volumeBlocks))
	both16(pvd[120:124], 1)
	both16(pvd[124:128], 1)
	both16(pvd[128:132], sector)
	both32(pvd[132:140], 10)
	binary.LittleEndian.PutUint32(pvd[140:144], 18)
	binary.BigEndian.PutUint32(pvd[148:152], 19)
	root := record([]byte{0}, rootBlock, sector, true, nil)
	copy(pvd[156:190], root)
	for _, bounds := range [][2]int{{190, 318}, {318, 446}, {446, 574}, {574, 702}, {702, 739}, {739, 776}, {776, 813}} {
		padded(pvd[bounds[0]:bounds[1]], "")
	}
	padded(pvd[574:702], "TENKACLOUD GUEST SEED")
	for _, offset := range []int{813, 830} {
		copy(pvd[offset:offset+17], []byte("1970010100000000\x00"))
	}
	for _, offset := range []int{847, 864} {
		copy(pvd[offset:offset+17], []byte("0000000000000000\x00"))
	}
	pvd[881] = 1
	descriptor(image[17*sector:18*sector], 255)
	for i, block := range []int{18, 19} {
		table := image[block*sector : (block+1)*sector]
		table[0] = 1
		if i == 0 {
			binary.LittleEndian.PutUint32(table[2:6], rootBlock)
			binary.LittleEndian.PutUint16(table[6:8], 1)
		} else {
			binary.BigEndian.PutUint32(table[2:6], rootBlock)
			binary.BigEndian.PutUint16(table[6:8], 1)
		}
	}
	// SP negotiates SUSP; ER identifies the Rock Ridge extension. Only a single
	// directory and fixed regular files are emitted; there are no links or paths.
	susp := append([]byte{'S', 'P', 7, 1, 0xbe, 0xef, 0}, posix(true)...)
	susp = append(susp, []byte{'E', 'R', 18, 1, 10, 0, 0, 1, 'R', 'R', 'I', 'P', '_', '1', '9', '9', '1', 'A'}...)
	entries := [][]byte{
		record([]byte{0}, rootBlock, sector, true, susp),
		record([]byte{1}, rootBlock, sector, true, posix(true)),
		record([]byte("META_DAT.;1"), metaBlock, len(metadata), false, named("meta-data")),
	}
	if len(network) != 0 {
		entries = append(entries, record([]byte("NETWORK.;1"), networkBlock, len(network), false, named("network-config")))
	}
	// ISO9660 records are sorted by identifier, independently of file extents.
	entries = append(entries, record([]byte("USER_DAT.;1"), userBlock, len(userData), false, named("user-data")))
	dir := image[rootBlock*sector : (rootBlock+1)*sector]
	offset := 0
	for _, entry := range entries {
		copy(dir[offset:], entry)
		offset += len(entry)
	}
	copy(image[metaBlock*sector:], metadata)
	copy(image[userBlock*sector:], userData)
	copy(image[networkBlock*sector:], network)
	return image, nil
}

func blocks(size int) int              { return (size + sector - 1) / sector }
func descriptor(dst []byte, kind byte) { dst[0] = kind; copy(dst[1:6], "CD001"); dst[6] = 1 }
func padded(dst []byte, value string) {
	for i := range dst {
		dst[i] = ' '
	}
	copy(dst, value)
}
func both16(dst []byte, v uint16) {
	binary.LittleEndian.PutUint16(dst[:2], v)
	binary.BigEndian.PutUint16(dst[2:4], v)
}
func both32(dst []byte, v uint32) {
	binary.LittleEndian.PutUint32(dst[:4], v)
	binary.BigEndian.PutUint32(dst[4:8], v)
}
func record(name []byte, extent, size int, directory bool, system []byte) []byte {
	length := 33 + len(name)
	if length%2 != 0 {
		length++
	}
	out := make([]byte, length+len(system))
	out[0] = byte(len(out))
	both32(out[2:10], uint32(extent))
	both32(out[10:18], uint32(size))
	copy(out[18:25], []byte{70, 1, 1, 0, 0, 0, 0})
	if directory {
		out[25] = 2
	}
	both16(out[28:32], 1)
	out[32] = byte(len(name))
	copy(out[33:], name)
	copy(out[length:], system)
	return out
}
func posix(directory bool) []byte {
	out := make([]byte, 41)
	copy(out, []byte{'R', 'R', 5, 1, 1, 'P', 'X', 36, 1})
	mode, links := uint32(0100444), uint32(1)
	if directory {
		mode = 0040555
		links = 2
	}
	both32(out[9:17], mode)
	both32(out[17:25], links)
	return out
}
func named(name string) []byte {
	out := posix(false)
	out[4] |= 8
	out = append(out, 'N', 'M', byte(5+len(name)), 1, 0)
	return append(out, []byte(name)...)
}
