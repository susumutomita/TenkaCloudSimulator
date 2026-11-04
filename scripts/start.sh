#!/bin/sh
# Source-checking developer entry point. The built binary has no shell dependency.
set -eu

fail() { printf '%s\n' "make start: $*" >&2; exit 1; }
repo=$(CDPATH= cd "$(dirname "$0")/.." && pwd)
cd "$repo"
go_command=${GO-go}
listen=${START_LISTEN-127.0.0.1:4566}
state=${START_STATE-./practice.db}
token_file=${START_TOKEN_FILE-./simulator-token}
[ -n "$go_command" ] || fail 'GO must name a Go executable.'
[ -n "$listen" ] || fail 'START_LISTEN must be a numeric loopback address and port.'
[ -n "$state" ] || fail 'START_STATE must name a SQLite file (or explicit :memory:).'
[ -n "$token_file" ] || fail 'START_TOKEN_FILE must name a private token file.'
[ "$state" != "$token_file" ] || fail 'State and operator token must use different files.'
command -v "$go_command" >/dev/null 2>&1 || fail 'Go is required to build current source, even when dist exists. Install Go 1.26 or set GO=/path/to/go. A verified prebuilt binary can be run directly; see docs/start.md.'

# Rebuild every start using Go's incremental cache. Never fall back to an older
# executable after a failed compiler/source check, and install only a full build.
umask 077
mkdir -p ./dist
[ ! -d ./dist/tenkacloud-simulator ] || fail 'dist/tenkacloud-simulator is a directory; choose a clean build output path.'
build_file=$(mktemp ./dist/.simulator-start.XXXXXX)
cleanup() { if [ -n "$build_file" ]; then rm -f "$build_file"; fi; }
trap cleanup 0
trap 'exit 130' INT
trap 'exit 143' TERM
printf '%s\n' 'Building current simulator source…'
if ! GOOS= GOARCH= CGO_ENABLED=0 "$go_command" build -trimpath -ldflags='-s -w' -o "$build_file" ./cmd/simulator; then
    fail 'Build failed; an existing binary was not started. State and token files were not changed.'
fi
[ -s "$build_file" ] && [ -x "$build_file" ] || fail 'Go did not produce an executable; an existing binary was not started.'
mv -f "$build_file" ./dist/tenkacloud-simulator
build_file=
# These directories may be new overrides; existing files and permissions are not
# reset. The CLI validates/reuses a private regular token file, without rotation.
if [ "$state" != ':memory:' ]; then mkdir -p -- "$(dirname -- "$state")"; fi
mkdir -p -- "$(dirname -- "$token_file")"
set -- "--listen=$listen" "--state=$state" "--token-file=$token_file"
# Printing is deliberate for an interactive developer. CI (even CI='') and
# stdout redirection suppress it; the CLI independently enforces the same guard.
if [ -t 1 ] && [ "${CI+x}" != x ]; then set -- "$@" --show-token; fi
trap - 0 INT TERM
exec ./dist/tenkacloud-simulator "$@"
