package entraidp

import (
	"crypto/rand"
	"fmt"
)

// Directory is one disposable Azure-shaped tenant and synthetic account. These
// identities do not authorize any Simulator, Graph or ARM operation.
type Directory struct {
	TenantID string
	ObjectID string
}

func newDirectory() Directory {
	return Directory{TenantID: newGUID(), ObjectID: newGUID()}
}

func newGUID() string {
	var id [16]byte
	rand.Read(id[:])
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
}
