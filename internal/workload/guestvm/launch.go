package guestvm

import (
	"context"

	"github.com/susumutomita/TenkaCloudSimulator/internal/workload/guestprocess"
)

// Launch passes only this deployment's fixed verified-asset invocation to the
// concrete process owner. A trusted lifecycle coordinator MUST durably bind the
// receipt and fence its operation before calling this method. This adapter does
// not supply that coordinator and must not be exposed as a participant API.
//
// Calling Launch attempts a real host process start. The plan leaves guest CPUs
// paused, but that is not durable containment. Success requires subsequent UUID
// verification, managed activation and all runtime acceptance checks. Retain the
// returned handle and all prepared assets until exact-process exit is observed.
// Cancellation/uncertain launch is never permission to replay or remove files.
// No default simulator/provider path calls this method.
func (p *Prepared) Launch(ctx context.Context) (*guestprocess.Process, error) {
	invocation := p.Invocation()
	return guestprocess.Launch(ctx, guestprocess.Spec{Executable: invocation.Executable, Args: invocation.Args, Env: invocation.Env})
}
