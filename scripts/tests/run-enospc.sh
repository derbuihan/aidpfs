#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
fixture="$(mktemp -d /tmp/aidpfs-enospc.XXXXXX)"
test_bin="$(mktemp /tmp/aidpfs-enospc-test.XXXXXX)"
mounted=0
cleanup() {
	status=$?
	if [[ "$mounted" == 1 ]]; then
		sudo umount "$fixture" || status=1
	fi
	rmdir "$fixture" 2>/dev/null || true
	rm -f "$test_bin"
	exit "$status"
}
trap cleanup EXIT

if [[ ! -r /dev/fuse || ! -w /dev/fuse ]]; then
	echo "real ENOSPC FUSE check requires read/write access to /dev/fuse" >&2
	exit 2
fi
if ! sudo -n true; then
	echo "real ENOSPC FUSE check requires passwordless sudo for a temporary tmpfs mount" >&2
	exit 2
fi

cd "$repo_root"
go test -c -o "$test_bin" ./internal/mount
sudo mount -t tmpfs -o size=64m tmpfs "$fixture"
mounted=1
sudo chown "$(id -u):$(id -g)" "$fixture"
TMPDIR="$fixture" AIDPFS_TEST_ENOSPC=1 "$test_bin" -test.run='^TestExperimentalMountENOSPC$' -test.v
