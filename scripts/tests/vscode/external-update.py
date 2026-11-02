#!/usr/bin/env python3
"""Update only the dedicated FILE used by the isolated VS Code E2E probe."""
import os
import re
import sys
from urllib.parse import quote

import oci
import requests


root = os.environ.get("AIDPFS_E2E_REMOTE_ROOT", "")
if not re.fullmatch(r"/Workspace/Shared/aidpfs-probe-[0-9a-f]{32}", root):
    raise SystemExit("invalid synthetic fixture root")
platform = os.environ.get("AIDPFS_PROBE_PLATFORM", "")
workspace = os.environ.get("AIDPFS_PROBE_WORKSPACE", "")
profile = os.environ.get("AIDPFS_PROBE_PROFILE", "")
endpoint = os.environ.get("AIDPFS_PROBE_ENDPOINT", "")
if not all((platform, workspace, profile, endpoint)) or not endpoint.startswith("https://"):
    raise SystemExit("incomplete AIDP configuration")
if len(sys.argv) != 1:
    raise SystemExit("unexpected arguments")

config = oci.config.from_file(file_location=os.environ.get("OCI_CONFIG_FILE") or None, profile_name=profile)
signer = oci.signer.Signer(
    tenancy=config["tenancy"], user=config["user"], fingerprint=config["fingerprint"],
    private_key_file_location=config["key_file"], pass_phrase=config.get("pass_phrase"),
)
base = endpoint.rstrip("/") + "/20260430/aiDataPlatforms/" + quote(platform, safe="")
base += "/workspaces/" + quote(workspace, safe="")
remote_path = root + "/vscode-external.txt"
url = base + "/notebook/api/contents/" + quote(remote_path.lstrip("/"), safe="")
response = requests.put(
    url, auth=signer,
    json={"type": "file", "format": "text", "content": "aidpfs-vscode-external-update\n"},
    timeout=30, allow_redirects=False,
)
if response.status_code != 200:
    raise SystemExit("external synthetic FILE update failed with HTTP " + str(response.status_code))
print("external synthetic FILE update completed: HTTP 200")
