#!/usr/bin/env bash
set -euo pipefail
umask 077
case "$1" in inside|diagnose) ;; *) exit 2 ;; esac
runner_uid=$(id -u)
parent_net=$(stat -Lc %i /proc/self/ns/net)
parent_user=$(stat -Lc %i /proc/self/ns/user)
exec unshare --user --map-current-user --net --keep-caps \
  sh -eu -c 'ip link set lo up; exec setpriv --no-new-privs --inh-caps=-all --ambient-caps=-all --bounding-set=-all "$@"' \
  simpledb-terraform "$PYTHON" -IB "$VERIFY_DIR/verify.py" "$1" "$runner_uid" "$parent_net" "$parent_user"
