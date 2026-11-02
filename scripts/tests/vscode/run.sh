#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../.." && pwd)"
workspace="${1:-${AIDPFS_E2E_WORKSPACE:-}}"
if [[ -z "$workspace" || ! -d "$workspace" ]]; then
  echo "Usage: scripts/tests/vscode/run.sh /path/to/mounted/synthetic/workspace" >&2
  exit 2
fi
if [[ -z "${AIDPFS_E2E_FILE:-}" ]]; then
  echo "AIDPFS_E2E_FILE must name a synthetic regular file in the workspace" >&2
  exit 2
fi

cd "${repo_root}/scripts/tests/vscode"
npm ci --no-audit --no-fund
npm run build
export AIDPFS_E2E_WORKSPACE="$workspace"
rm -rf /tmp/aidpfs-vscode
mkdir -p /tmp/aidpfs-vscode/user-data /tmp/aidpfs-vscode/extensions
xvfb-run -a npm test
