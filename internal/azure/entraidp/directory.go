package entraidp

import (
	"crypto/rand"
	"errors"
	"fmt"
	"regexp"
)

// Directory is one disposable Azure-shaped tenant and synthetic account. These
// identities do not authorize any Simulator, Graph or ARM operation.
type Directory struct {
	TenantID string
	ObjectID string
	email    string
}

// Email is the immutable, non-deliverable address of the generated account.
// It is available to the trusted fixture launcher, not a public metadata field.
func (d Directory) Email() string { return d.email }

var syntheticGUID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func syntheticEmail(tenant, object string) (string, error) {
	if !syntheticGUID.MatchString(tenant) || !syntheticGUID.MatchString(object) {
		return "", errors.New("noncanonical synthetic directory identity")
	}
	// Both UUIDs are canonical ASCII. The mailbox and DNS labels are below
	// standard length bounds; .invalid never denotes a delivery destination.
	return "user-" + object + "@tenant-" + tenant + ".entra.invalid", nil
}

func newDirectory() (Directory, error) {
	d := Directory{TenantID: newGUID(), ObjectID: newGUID()}
	var err error
	d.email, err = syntheticEmail(d.TenantID, d.ObjectID)
	return d, err
}

func newGUID() string {
	var id [16]byte
	rand.Read(id[:])
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
}
