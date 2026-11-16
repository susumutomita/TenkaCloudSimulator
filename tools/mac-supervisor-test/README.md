# Transient benign launchd experiment: blocked before launch

This is a development-only, non-VM acceptance experiment. It must NOT be run
again automatically: its initial bootstrap returned exit 5 and work stopped.
No alternate domain, permission escalation, wrapper, VM execution or retry was
used. No runtime capability was enabled.

2026-11-13 on Darwin ARM64:
- `launchctl print gui/501` was readable.
- One dedicated `launchctl bootstrap` returned 5 before kickstart.
- An exact-label read afterward reported `Could not find service`.
- No child/supervisor observation files were created.
- Initial runner captured but did not preserve command stderr when propagating
  CalledProcessError, so the detailed cause is unknown. Do not label exit 5 alone
  as a confirmed permission denial or guess a remedy.

The runner now retains CalledProcessError output for a future authorized attempt;
that logging change has NOT been used to retry bootstrap. Results must distinguish
successful registration, actual child initialization, same-group observation,
child absence, no restart, and confirmed service removal. No such successful
acceptance result exists. The process-group guarantee remains unverified here.

This fixture uses a Python child ignoring TERM and exiting itself after 15 seconds.
The supervisor remains its parent in the same process group, and the proposed job
has RunAtLoad/KeepAlive/AbandonProcessGroup false. Controller crash, stopped child,
group escape and adversarial workloads are NOT tested or contained by this script.
A non-running launchd state or absence check is not proof of a successful kernel
reap by the original parent. Do not use this experiment as Battle qualification.

## Subsequent offline error-handling correction

The CLI remains blocked and no registration was retried. Seven mocked tests now
cover failed bootstrap with confirmed exact-label absence, bootstrap timeout with
partial registration, cleanup failure preserving both errors, and unreadable
registration state, state-inspection timeout, bounded error text and evidence-write
disk failure without a success receipt. `subprocess.run` is replaced for every test: no launchctl,
child process or VM is run. Unknown state remains unknown; no success is inferred.
All attempted registrations would be inspected by exact generated label. If a
partial registration is observed, only that label is cleaned up; registration is
never retried. This is currently tested with mocks only. Operation and cleanup
exceptions are separately preserved with bounded error text, even when bootout
fails. A failed result-file write still raises and cannot become a success receipt.
These tests do not recover the lost stderr from the original real failure or
qualify macOS process ownership. Real testing remains blocked.
