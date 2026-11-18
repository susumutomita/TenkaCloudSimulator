#!/bin/bash
# Offline image builder for the Battle rehearsal guest (see ../README.md).
# NoCloud user-data for ONE throwaway boot of a COPY of the official AL2023 KVM
# image. It never runs in a Battle VM and never changes Battle UserData.
#
# Set these three values for the image release and relay overlay being built:
RELAY=http://10.0.2.2:8099   # repo_relay.py on the builder host, reached through QEMU user networking
GUID=707b7dfd96f868bd1650473d34d918a106283766d54260bc446abe9beefb6523   # mirror.list GUID of AL2023 2023.12.20261112
OVERLAY_SHA256=f553a660f9cd672dc30867b90f28c283e565ad9bf7ee98a30a255e1fc3a789d9   # session-overlay.py receipt
main() {
  exec > >(tee /dev/ttyS0) 2>&1
  set -eux
  echo "BUILDER-START $(date -u +%FT%TZ)"
  OFFLINE=/var/local/tenkacloud-offline-repo
  cat /etc/os-release
  uname -r
  getenforce || true
  cat /proc/cmdline
  ls -la /etc/yum.repos.d
  cat /etc/yum.repos.d/*.repo
  cat /etc/dnf/vars/* || true
  rpm -qa | sort > /root/rpm-before.txt
  wc -l /root/rpm-before.txt
  rpm -q qemu-guest-agent nginx python3 cloud-init createrepo_c || true
  ip -br addr
  ls -la /etc/systemd/network || true

  cat > /etc/yum.repos.d/tenkacloud-builder.repo <<REPO
[tenkacloud-builder]
name=Loopback relay of the official AL2023 core repository (build time only)
baseurl=$RELAY/cdn/al2023/core/guids/$GUID/x86_64/
enabled=1
gpgcheck=1
gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-amazon-linux-2023
REPO
  DNF="dnf --disablerepo=* --enablerepo=tenkacloud-builder --setopt=install_weak_deps=False"

  # Control-plane prerequisite: the private QGA channel (analogous to a preinstalled agent).
  $DNF install -y qemu-guest-agent
  systemctl cat qemu-guest-agent || true
  # Run Command needs guest-exec/guest-exec-status; this release ships no RPC filter.
  if grep -Eq '^(QEMU_GA_ARGS|BLACKLIST_RPC|FILTER_RPC_ARGS)=.*guest-exec' /etc/sysconfig/qemu-ga; then
    echo "guest-exec is filtered in this image release"; exit 1
  fi
  systemctl enable qemu-guest-agent || true
  cat /usr/lib/udev/rules.d/*qemu-guest-agent* || true

  # Offline repository holding exactly what the unchanged Battle UserData installs
  # (nginx and its missing dependencies; python3 ships in the base image).
  # dnf deletes --downloadonly packages after its next successful transaction, so
  # every other transaction happens first and the RPMs are copied out of dnf's view.
  $DNF install -y createrepo_c
  mkdir -p $OFFLINE /root/offline-download
  $DNF install -y --downloadonly --downloaddir=/root/offline-download nginx python3
  cp -v /root/offline-download/*.rpm $OFFLINE/
  rm -rf /root/offline-download
  rpm -K $OFFLINE/*.rpm
  createrepo_c $OFFLINE
  ls -la $OFFLINE
  test "$(ls $OFFLINE/*.rpm | wc -l)" -ge 4
  rpm -e createrepo_c createrepo_c-libs
  rm -f /etc/yum.repos.d/tenkacloud-builder.repo
  for f in /etc/yum.repos.d/*.repo; do sed -i 's/^enabled *= *1/enabled=0/' "$f"; done
  cat > /etc/yum.repos.d/tenkacloud-offline.repo <<REPO
[tenkacloud-offline]
name=Offline subset of the official AL2023 core repository (signature-checked)
baseurl=file://$OFFLINE
enabled=1
gpgcheck=1
gpgkey=file:///etc/pki/rpm-gpg/RPM-GPG-KEY-amazon-linux-2023
metadata_expire=never
REPO
  dnf clean all
  dnf repolist -v | head -40
  rpm -q nginx && { echo "nginx must not be preinstalled"; exit 1; } || true
  # The offline repository must resolve exactly what the Battle UserData asks for.
  dnf -q repoquery --available nginx | grep -q '^nginx-'
  dnf install -y --assumeno nginx python3 2>&1 | tee /root/dryrun.txt || true
  grep -q 'nginx-core' /root/dryrun.txt
  rm -f /root/dryrun.txt

  # Reviewed guest-only session relay overlay (scripts/guest-image/SESSION-IMAGE.md).
  curl -fsS -o /root/session-overlay.tar $RELAY/local/session-overlay.tar
  echo "$OVERLAY_SHA256  /root/session-overlay.tar" | sha256sum -c
  tar -C / --no-same-owner -xf /root/session-overlay.tar
  rm -f /root/session-overlay.tar
  restorecon -Rv /usr/libexec/tenkacloud-session-relay /usr/lib/systemd/system/tenkacloud-session-relay.service /etc/tenkacloud /etc/systemd/system/multi-user.target.wants /usr/share/licenses/tenkacloud-session-relay || true
  ls -laZ /usr/libexec/tenkacloud-session-relay /etc/tenkacloud/session-relay-image /etc/systemd/system/multi-user.target.wants/
  sha256sum /usr/libexec/tenkacloud-session-relay

  rpm -qa | sort > /root/rpm-after.txt
  diff /root/rpm-before.txt /root/rpm-after.txt || true
  rm -f /root/rpm-before.txt /root/rpm-after.txt
  systemd-analyze || true
  systemd-analyze blame | head -25 || true
  ls -la /etc/systemd/network || true
  rm -f /etc/systemd/network/*cloud-init* || true
  cat /etc/hostname || true
  rm -f /etc/hostname
  cloud-init clean --logs
  sync
  echo "BUILDER-DONE $(date -u +%FT%TZ)"
  poweroff
}
main
