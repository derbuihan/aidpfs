#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
oci_dir="${AIDPFS_OCI_CONFIG_DIR:-${HOME}/.oci}"
if [[ ! -r "${oci_dir}/config" ]]; then
  echo "OCI config is not readable" >&2
  exit 2
fi
if [[ ! -r /dev/fuse || ! -w /dev/fuse ]]; then
  echo "/dev/fuse must be readable and writable" >&2
  exit 2
fi

export AIDPFS_OCI_CONFIG_DIR="$(cd "$oci_dir" && pwd)"
cd "$repo_root"
compose=(docker compose -f docker-compose.vscode.yml)
if [[ "${1:-}" == "--build" ]]; then
  "${compose[@]}" build vscode-test
  shift
fi
if [[ "${1:-}" == "--help" || "${1:-}" == "-h" ]]; then
  echo "Usage: scripts/test-vscode-docker.sh [--build] [-- go-command ...]"
  echo "Without a command, runs formatting, vet, race tests, and Linux build."
  exit 0
fi
if [[ "${1:-}" == "--" ]]; then
  shift
fi
if (($#)); then
  "${compose[@]}" run --rm vscode-test "$@"
else
  "${compose[@]}" run --rm vscode-test bash -c 'scripts/tests/run.sh'
fi
