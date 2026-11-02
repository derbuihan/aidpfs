#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
: "${AIDPFS_PROBE_PLATFORM:?workspace platform is required}"
: "${AIDPFS_PROBE_WORKSPACE:?workspace key is required}"
: "${AIDPFS_PROBE_PROFILE:=OCI_TOKYO}"
: "${AIDPFS_PROBE_REGION:=ap-osaka-1}"
: "${AIDPFS_PROBE_ENDPOINT:=https://aidp.ap-osaka-1.oci.oraclecloud.com}"

cd "$repo_root"
mount_binary="$(mktemp /tmp/aidpfs-vscode.XXXXXX)"
journal_dir="$(mktemp -d /tmp/aidpfs-vscode-journal.XXXXXX)"
chmod 700 "$journal_dir"
trap 'rm -f "$mount_binary"' EXIT
go build -buildvcs=false -o "$mount_binary" ./cmd/aidpfs
python3 scripts/aidp-write-probe.py \
  --platform-id "$AIDPFS_PROBE_PLATFORM" \
  --workspace-key "$AIDPFS_PROBE_WORKSPACE" \
  --profile "$AIDPFS_PROBE_PROFILE" \
  --region "$AIDPFS_PROBE_REGION" \
  --endpoint "$AIDPFS_PROBE_ENDPOINT" \
  --journal "$journal_dir/probe.json" \
  --write-mount-binary "$mount_binary" \
  --vscode-check --go-par-test
rm -rf -- "$journal_dir"
