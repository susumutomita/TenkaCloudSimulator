package guestvm

import (
	"context"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/qmp"
)

// OpenControl binds an already-authorized, exclusively owned QMP transport to
// the UUID generated in this deployment receipt. It does not dial a socket,
// discover a process, resume CPUs, or establish durable lease ownership.
// The transport is consumed even when its peer reports a different VM identity.
// A lifecycle owner must independently establish its private transport/process
// authority before calling this method; UUID matching is an additional check.
func (p *Prepared) OpenControl(ctx context.Context, transport qmp.Transport, options qmp.Options) (*qmp.Client, error) {
	return qmp.New(ctx, transport, p.record.UUID, options)
}
