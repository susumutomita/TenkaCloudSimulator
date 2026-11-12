# Canonical interactive guest channel

The optional session backend uses the same retained EC2 runtime and VM owner as
Battle probes. It resolves only an exact live retained instance/stack entry with
matching durable ready plan, original lifetime, immutable prepared UUID and an
explicitly selected relay-image hash. It dials that preparation's fixed private
session endpoint; caller paths, caller UUIDs, fresh VM launch and QGA guest-exec
are not fallback implementations. The guest's UUID Hello is required before a
Driver is returned. That handshake alone does not start a PTY: Start must obtain
the guest relay's Ready response.

This slice bounds each VM to eight acquired channel generations. Resume retains
its same Driver/PTY. A fresh Start requires the previous guest's actual Exit
acknowledgment, successful driver/read-loop joins and successful retained stream
close, then a fresh UUID Hello on the same VM. No automatic dial retry occurs.
The guest relay exits after the PTY ends and its pinned systemd unit restarts it;
this is a source contract still requiring real image/VM qualification. Failed or
unknown attempts remain tombstones and block a fresh generation. At most 1024
instance entries / 8192 total generation receipts are retained; cleanup does not
delete or recycle historical receipts.

Owner invalidation closes the private channel and blocks further admitted IO.
An already in-flight write may have partial effects; read results are rechecked
and discarded after invalidation. Local close and reader joins are separate from
an actual guest PTY Exit acknowledgment. Runtime shutdown retains the database and
receipts if those local joins or close results are unknown. No channel close is
asserted to prove guest process death or external VM containment.

These source contracts and memory tests do not qualify real Unix socket access,
VM boot, installed guest relay, guest PTY operation or provider isolation. The
separate QGA endpoint is unchanged and existing execution holds still apply.
