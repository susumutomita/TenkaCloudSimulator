# Pinned session relay image build

This is an offline image build procedure, not simulator startup. It has not been
executed or qualified by the source-only tests. Never run the guest relay on the
host, and never change Battle UserData to install or impersonate this service.

1. Select the reviewed Linux/amd64 AL2023 base qcow2 and verify its recorded digest
   and publisher provenance. Work on a new independent image, not the base or a
   running deployment disk. Require bash, systemd and Linux PTY support in it.
2. The incoming relay source belongs to this repository's separately reviewed
   participant-session increment, not this hook alone. At this review boundary its
   `guest/ssm-relay/main_linux.go` SHA256 is
   `805091350aa64c92b4350a79fbb01ceba069a76b3d0d18caa0c0c020543cdffa`,
   unit SHA256 `2b1f22e662cf42c51f3423dd3052a8f13fdcb6a7156b0274da3962d69d5c4bf0`,
   and repository MIT LICENSE SHA256
   `1476f81dd9aa0aa3ec6589fd3eaf509fc1b584c0e1f581589735dc609aa7db81`.
   Do not build from a missing directory or silently substitute other source.
   First integrate the reviewed relay and its `internal/workload/guestsession`
   dependencies, then record the full immutable integrated revision and all
   dependency hashes. From that repository root build `./guest/ssm-relay` with the repository's pinned Go compiler, Linux/amd64,
   `CGO_ENABLED=0`, `-trimpath` and `-buildvcs=false`. Record source commit, toolchain
   digest and resulting binary SHA256; review the service unit digest separately.
3. Run `session-overlay.py` with those exact binary, unit, repository MIT license and reviewed digests. It
   packages the guest binary, unit, MIT license, enablement symlink and image marker without
   starting a process, VM or relay. It refuses existing output and digest drift.
4. In the separately authorized offline image-builder environment, use the pinned
   image tool to apply this tar to the new disk (for example `virt-customize -a
   NEW.qcow2 --network=off --tar-in OVERLAY.tar:/`). Record tool/version/digest,
   input/output disk digests, overlay digest and complete build log. Do not invoke
   this step where VM/image execution is held. Do not perform package downloads
   or replace the user's script. Any required packages must be provenance-pinned
   build inputs rather than runtime network installation introduced by this hook.
5. Place the resulting disk in the existing verified guest bundle and regenerate
   its file/archive hashes. Set trusted `SessionImageSHA256` to that exact base
   disk digest. No participant request may supply this setting. Preserve the build
   receipt alongside release provenance; the setting is a build selection, not
   attestation that a PTY exists or proof of guest isolation.

Preparation records the selected disk digest and adds only the fixed private
`session.sock` / `org.tenkacloud.session.0` channel. The private QGA channel remains
unchanged. The canonical owner must retain this stream and require a real TCS1
Hello matching its exact Prepared UUID before exposing any session. The image
marker, host metadata, a matching digest, or a successful QGA response cannot
replace that handshake. Start additionally requires the guest PTY Ready result.
Actual image boot, guest relay/PTY operation, containment and cleanup qualification
remain required; packaging this overlay is not such evidence.

Before redistributing any resulting disk/bundle, review base-image, firmware,
emulator, guest package and relay licensing/distribution permissions. Include all
required licenses, notices and source offers in the release; the relay MIT license
payload alone does not satisfy obligations of other bundled components. Record
that review with the build provenance. No redistribution approval is inferred.
