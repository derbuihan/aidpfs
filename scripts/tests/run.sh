#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$repo_root"

if [[ "${1:-}" == "--live" ]]; then
	if [[ $# -ne 1 ]] || [[ ! "${AIDPFS_PROBE_ROOT:-}" =~ ^/Workspace/Shared/aidpfs-probe-[0-9a-f]{32}$ ]]; then
		echo "--live requires AIDPFS_PROBE_ROOT=/Workspace/Shared/aidpfs-probe-<32 lowercase hex digits> and the AIDPFS_PROBE_* settings" >&2
		exit 2
	fi
	if [[ ! -r /dev/fuse || ! -w /dev/fuse ]]; then
		echo "--live requires read/write access to /dev/fuse" >&2
		exit 2
	fi
	: "${AIDPFS_PROBE_PLATFORM:?missing AIDPFS_PROBE_PLATFORM}"
	: "${AIDPFS_PROBE_WORKSPACE:?missing AIDPFS_PROBE_WORKSPACE}"
	: "${AIDPFS_PROBE_PROFILE:?missing AIDPFS_PROBE_PROFILE}"
	: "${AIDPFS_PROBE_REGION:?missing AIDPFS_PROBE_REGION}"
	: "${AIDPFS_PROBE_ENDPOINT:?missing AIDPFS_PROBE_ENDPOINT}"
	AIDPFS_LIVE_PAR_PROBE=isolated go test -race -p=1 -count=1 -timeout=180s ./internal/aidp ./internal/mount -run '^TestLive(PAR|FUSE)IsolatedFixture$'
	exit
fi

if [[ $# -ne 0 ]]; then
	echo "usage: $0 [--live]" >&2
	exit 2
fi

files="$(gofmt -l $(rg --files -g '*.go'))"
if [[ -n "$files" ]]; then
	echo "Go files need formatting:" >&2
	echo "$files" >&2
	exit 1
fi

go vet ./...
go test -race -p=1 -count=1 -timeout=120s ./...
test_bin="$(mktemp /tmp/aidpfs-test.XXXXXX)"
trap 'rm -f "$test_bin"' EXIT
go build -o "$test_bin" ./cmd/aidpfs
