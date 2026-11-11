# Guest-only PTY relay (source prerequisite)

This program must be built into a reviewed, checksum-pinned Linux guest image.
It is not a host command runner. No installation or PTY/process execution has
been performed by this source change. Do not execute this binary on the host.

Required image inputs: `/bin/bash`, Linux PTY support, systemd, the binary at
`/usr/libexec/tenkacloud-session-relay`, the supplied service unit and root-owned
`/etc/tenkacloud/session-relay-image` containing `tenkacloud-guest-session-v1\n`.
The VM owner must add a separate fixed private virtio serial channel named
`org.tenkacloud.session.0`; the QGA channel is not reused. UserData is unchanged.

The private TCS1 framing is not the AWS ssmmessages data-channel wire protocol.
Native SSM lifecycle responses alone do not establish AWS CLI Session Manager
plugin compatibility. Guest UUID, session identity, sequence, payload, dimensions,
15-minute lifetime and 4 MiB per-direction limits are enforced. A channel closure
kills the attached guest PTY process group, not the host or VM. Reopening and
resuming a live PTY require retained host/guest state; they must not spawn a new
shell and call it a resumed session.

Current source still needs independent review, memory-only protocol tests and
approved image/VM qualification. Physical execution remains unverified.

A successful private session Exit is not an exit-zero relay process. After its
joined session the relay returns EOF, and main deliberately exits 1 so the
supplied Restart=on-failure service starts a fresh codec/Hello generation. This
is an image-source contract, not evidence that systemd or virtio reconnect was
executed here. A host may request a new generation only after the previous
guest Exit and transport/read-loop cleanup are all positively acknowledged.
Unknown debt never permits reconnect or reassigning an old session's owner.
