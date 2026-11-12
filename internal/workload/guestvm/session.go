package guestvm

import (
	"context"
	"errors"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestbundle"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestlease"
	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestseed"
	"path/filepath"
)

// ValidateSessionImage binds explicit operator-reviewed relay image provenance
// to the already verified base image. A matching hash is not a live relay proof:
// the retained owner must still require the guest channel's exact UUID Hello.
func ValidateSessionImage(bundle *guestbundle.Bundle, layout Layout, expected string) error {
	if bundle == nil || !fencePattern.MatchString(expected) {
		return errors.New("pinned session image required")
	}
	info, err := bundle.Info(layout.BaseDisk)
	if err != nil {
		return err
	}
	if info.SHA256 != expected {
		return errors.New("session image pin mismatch")
	}
	return nil
}

func PrepareWithNetworkAndSession(ctx context.Context, parent string, bundle *guestbundle.Bundle, layout Layout, intent guestlease.Intent, userData []byte, network guestseed.IPv4Network, imageSHA256 string) (*Prepared, error) {
	return prepare(ctx, parent, bundle, layout, intent, userData, &network, imageSHA256)
}

// SessionSocket is a distinct private guest data endpoint, never a QGA fallback.
// Empty means this deployment did not opt into a pinned relay image.
func (p *Prepared) SessionSocket() string {
	if p == nil || p.record.SessionImageSHA256 == "" {
		return ""
	}
	return filepath.Join(p.directory, "session.sock")
}

func validPrivateEndpoints(directory string, session bool) bool {
	for _, name := range []string{"qmp.sock", "qga.sock"} {
		if len(filepath.Join(directory, name)) > 107 {
			return false
		}
	}
	return !session || len(filepath.Join(directory, "session.sock")) <= 107
}
