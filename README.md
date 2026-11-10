# aidpfs

`aidpfs` is an experimental Linux FUSE filesystem for reading one Oracle AI Data Platform (AIDP) Workbench workspace subtree. It uses the WorkspaceObject and notebook contents APIs with OCI API-key authentication. The supported scope and verification status are tracked in [TASK.md](TASK.md).

**The default mount is read-only.** Write mode supports regular FILE creation, modification, unlink and limited rename, plus directory creation. Notebook saving and directory deletion remain unsupported. FILE updates use conditional Object Storage PAR transfers; the tested AIDP contents and WorkspaceObject APIs accepted an incorrect `if-match`, so their headers are not treated as conflict protection.

## Requirements

- Linux, Go 1.26 or newer, `/dev/fuse`, and `fusermount3`; the user must be able to mount and unmount FUSE filesystems.
- An OCI API-key profile, permission to list workspace objects and read notebook contents, and network access to the AIDP endpoint. Other authentication methods have not been validated.
- The platform OCID and the **workspace key** returned by the workspace API. The key is not necessarily the workspace display name.
- An existing, empty local mount directory owned by the current user. The examples use `/Workspace`; select a subtree available to your identity. The `/` remote root is currently unsupported because its API path mapping is not established.

## Install the latest Linux preview

The `latest` GitHub pre-release is rebuilt from each successful push to `main`; it is a moving preview, not a stable version. Open [the latest release](https://github.com/derbuihan/aidpfs/releases/tag/latest) and download the `.deb` package matching `amd64` or `arm64`. On Debian or Ubuntu, install it with:

```sh
sudo apt install ./aidpfs_*_$(dpkg --print-architecture).deb
aidpfs --version
```

The package declares `fuse3` as an apt dependency. You still need a usable `/dev/fuse`, `fusermount3`, and an OCI API-key profile; installation does not configure AIDP credentials or grant FUSE permissions. The release also provides architecture-specific `.tar.gz` archives containing the binary and project notices, plus `SHA256SUMS` for verification. The latest workflow replaces prior GitHub Releases, so older preview assets are not retained there.

## Build and mount

```sh
make test
make build
mkdir -p "$HOME/aidp-mount"
./aidpfs --profile OCI_PROFILE --region REGION \
  --platform-id PLATFORM_OCID --workspace-key WORKSPACE_KEY \
  --remote-path /Workspace --mount-point "$HOME/aidp-mount"
```

The read-only command above is the default. For supported FILE writes, first create a private empty journal directory and use `--write --recovery-dir`:

```sh
install -d -m 700 "$HOME/aidpfs-recovery"
./aidpfs --profile OCI_PROFILE --region REGION \
  --platform-id PLATFORM_OCID --workspace-key WORKSPACE_KEY \
  --remote-path /Workspace/Shared --mount-point "$HOME/aidp-mount" \
  --write --recovery-dir "$HOME/aidpfs-recovery"
```

The process stays in the foreground. The endpoint defaults to `https://aidp.<region>.oci.oraclecloud.com`; use `--endpoint` for a different HTTPS AIDP origin. OCI configuration defaults to `~/.oci/config`; set `OCI_CONFIG_FILE` to choose another file. Credentials belong in OCI's configuration and private key files, not command arguments or logs. Use `--version` for the build version and `--help` for flags.

Unmount in a second terminal with `fusermount3 -u "$HOME/aidp-mount"`, or stop the foreground process with Ctrl-C. Verify the directory is no longer mounted before removing it. If signal-triggered unmount fails, the process reports the error and remains running; close processes using the mount and send Ctrl-C again or retry unmounting externally. Avoid force-unmounting an unrelated filesystem.

## Current behavior and limitations

- Mount access is restricted to the mounting user; directories are mode 0500 and files 0400. `allow_other`, volumes and multiple workspaces are not supported.
- Read-only file and directory ownership uses the mounting UID/GID, and a nonzero remote modification time is exposed as `mtime`. `chmod`, `chown`, timestamp changes, file writes and creation are rejected with `EROFS` on the read-only mount.
- `statfs` returns `EOPNOTSUPP`: the AIDP workspace API does not provide filesystem capacity or quota, and reporting zero-valued capacity fields would be misleading.
- Files are fetched in full on open and held in memory for that open. Each open gets a snapshot; a later open fetches again. Metadata and negative lookup TTLs are zero and direct I/O bypasses the kernel page cache. Concurrent changes between separate API operations can still produce inconsistent results.
- `stat` fetches file contents to report the size actually returned. API responses larger than 64 MiB fail with `EFBIG`, so binary file size limits depend on the encoded response. This approach can use substantial memory and API requests.
- Notebooks are exposed read-only as the JSON content returned by the contents API. Rich Notebook PUT/export tests showed normalized cell source, metadata/output representation changes, and PAR JSON differing from contents JSON. A Workbench UI test reproduced periodic autosave overwriting an external REST update without conflict detection; Notebook writes remain disabled. Preservation of every existing Notebook field has not been established.
- In the tested tenancy, creation and rename targets accept only ASCII letters, digits, `_`, `.`, `+`, `-`, `@` and space; Japanese and other non-ASCII letters, `#`, `%`, `(`, `~` and similar characters return `400 InvalidParameter`. The write mount rejects such new names locally with `EINVAL` before any request. Existing objects are listed and read under their remote names.
- The tested contents API infers the type of a FILE from its extension: without `type=file`, an extension-less FILE such as `Makefile` is reported as a directory and its content request fails with HTTP 400. aidpfs requests `type=file` for every FILE listed by the objects API.
- Unsupported object types cause directory listing to fail, rather than being silently hidden. The `objects` and contents APIs do not have identical path behavior; only the tested `/Workspace` subtree is known to work. Directory listing, permissions, and paths may differ across AIDP deployments.
- GET requests alone are retried for selected transient failures. The default mount does not use PAR. The sandbox AIDP/VS Code FILE E2E is verified, while multi-user access, other AIDP deployments, and large-file workloads are not. The Vim and Git checks do not cover ordinary editor settings or full Git workflows.

## FILE writes

The default mount remains read-only. To enable the supported FILE and directory operations, create a private, empty journal directory and pass `--write --recovery-dir /absolute/private/directory`; both options are required together. Notebook writes and `rmdir` remain unsupported. Keep the journal private because it contains file contents and recovery metadata.

**Rename and unlink require exclusive use of the affected workspace subtree.** Do not use another mount, Workbench, or another API client to modify it concurrently. The tested AIDP endpoints did not enforce `if-match`; a type or identity check before a request cannot prevent another client from replacing the target before deletion. This requirement is not enforced by a distributed lock. Rename supports a regular FILE only when the destination does not exist; replacement is rejected with `EOPNOTSUPP`, or `EEXIST` with `RENAME_NOREPLACE` (`mv --no-clobber`, `renameat2`). Other rename flags are rejected. If another client creates a FOLDER at the destination between the check and the request, AIDP moves the FILE into that folder instead of failing; the post-rename check then reports an unknown outcome. Rename or unlink of an open FUSE handle or unsaved FUSE buffer returns `EBUSY`. Editors may keep unsaved changes in their own memory after closing their FUSE handle; aidpfs cannot detect those buffers. Save and close such documents before renaming or deleting them. Folder and Notebook mutations are rejected. These restrictions are narrower than POSIX semantics and some editor save strategies will fail.

The write path is designed for single-user, single-mount FILE updates. It creates files without overwriting existing paths, journals create intent before making a remote request, and edits existing FILEs through a conditional Object Storage PAR upload followed by AIDP metadata completion and independent content verification. Unknown outcomes are never automatically retried. Conflicts or uncertain saves freeze the affected buffer and retain journal data for manual inspection. `fsync` reports the save result; close does not save or report a previous failure. Successful verification confirms the bytes at that version during the check, but does not lock out later updates from another client.

The implementation has a 64 MiB file limit and keeps each open file in memory. Direct I/O avoids the kernel page cache. Create and mkdir use durable local intents; an interrupted or uncertain create may have created the remote object, so inspect it before retrying. Startup requires an existing empty private recovery directory and locks it against another cooperating mount. Use `fsync` and check its result. The implementation does not provide cross-mount or Workbench locking, atomic append across clients, general crash-proof storage, or a guarantee against disk exhaustion. Read the recovery procedure below before enabling writes on valuable data.

| Operation | Result | Contract |
|---|---|---|
| Create regular FILE / mkdir | Supported, no overwrite | A durable local intent precedes one remote request. Inspect after an uncertain result before retrying. |
| Open existing FILE for write; write/truncate/append | Supported | Changes use a bounded local buffer and journal; conflicts preserve recoverable data. |
| `fsync` / synchronous write | Supported | Returns success after conditional PAR transfer, metadata completion and version/content verification. |
| `flush` | Supported | Each writable flush attempts the same save path and returns its error. |
| `release` / close | Handle release only | Does not start network I/O. Check `fsync` explicitly. |
| `unlink` | Supported for closed, clean FILEs | Single-writer only; an operation intent is retained and a successful response is followed by a parent listing check. |
| `rename` | Supported for closed, clean FILEs with absent destination | Same or different directory; `RENAME_NOREPLACE` is honored, no replacement, `RENAME_EXCHANGE`/`RENAME_WHITEOUT` return `EOPNOTSUPP`. Single-writer only. Success includes source/destination listing and content checks. |
| `rmdir`, folder/Notebook mutation | Rejected | Both tested DELETE endpoints remove a non-empty FOLDER with its children, so a safe `rmdir` is not possible; a directory passed to unlink/rename returns `EISDIR`. |
| New names outside the AIDP character set | `EINVAL` | Checked locally for create, mkdir and rename targets; nothing is sent. |
| chmod, chown, timestamp changes | `EOPNOTSUPP` | AIDP does not expose these POSIX metadata operations. |
| Open then unlink or rename | `EBUSY` | Close all handles and resolve dirty buffers first. |
| `forget` | Internal FUSE reference bookkeeping | Does not modify AIDP or recovery data. |
| POSIX advisory locks | Kernel-local only | Does not coordinate with Workbench, other mounts, or clients. |
| `statfs` | `EOPNOTSUPP` | AIDP capacity and quota are unavailable. |

The read-only mount rejects write access, `O_TRUNC`, and `O_APPEND` with `EROFS`. The write-enabled mount supports tested read/write, truncate, append (including open-file status-flag changes), and synchronous-write flags. The FUSE server uses direct I/O. Rename flags other than `RENAME_NOREPLACE` and unknown flags return `EOPNOTSUPP`; every Linux `open(2)` flag and broad concurrent workloads have not been tested.

AIDP contents and WorkspaceObject endpoints accepted wrong `if-match` values in the tested environment. Conditional Object Storage PAR upload rejected stale `If-Match` with HTTP 412; two concurrent uploads produced one accepted update and one conflict. Workbench and a REST client can still overwrite each other without detecting the conflict, so write mode is scoped to a single writer. `opc-retry-token` replay returned HTTP 500 on the tested create route while leaving one object and its payload intact; the implementation does not depend on retry-token replay or retry uncertain POSTs.

The write mount reuses a live FUSE inode for repeated lookups of the same path and type. This is not a verified AIDP object identity: an external delete and recreation may not be distinguishable. The AIDP-specific VS Code E2E tested open, search, edit, save, reopen, dirty buffer preservation, rejection with an open FUSE handle, closed FILE rename/delete, and an external update observed after reopening. Ordinary editor settings and multi-user use remain outside the tested contract.

For rename and unlink, an operation intent is synced before one API request. A definite rejection (HTTP 400, 401, 403, 404 or 409, or a failure before the request is sent) is recorded as `rejected` and returned as an errno; the tested tenant left source and destination unchanged in those cases, and later operations continue. If the outcome is unknown (transport error, timeout, 5xx including a concurrent rename's `Lock already held`, or a failed verification), the mount rejects subsequent create, mkdir, open, truncate, rename and unlink operations until unmounted; inspect the journal and remote paths rather than retrying blindly. In the tested tenant a rename whose response was lost had still been applied. A success response and readback are observations, not a lock or proof against later changes.

After the FUSE server stops, the CLI drains already entered operations and reports unresolved dirty buffers, invalid journal records or unconfirmed create intents. Prepared ancestors of a validated verified snapshot are treated as completed history; other branches remain unresolved. Check each write and `fsync` result. The recovery commands below inspect retained entries and export verified content without replaying or deleting journals.

## Recovery journal inspection

The write-enabled mount retains local recovery entries; the default read-only mount does not. For an existing private recovery directory, inspect entries and export verified journal contents to a **new** local file:

```sh
./aidpfs recovery list --dir /absolute/private/recovery-directory
./aidpfs recovery inspect-create --dir /absolute/private/recovery-directory \
  --id CREATE_INTENT_ID --region REGION --profile OCI_PROFILE
./aidpfs recovery inspect-mutation --dir /absolute/private/recovery-directory \
  --id MUTATION_INTENT_ID --region REGION --profile OCI_PROFILE
./aidpfs recovery lineage --dir /absolute/private/recovery-directory --id ENTRY_ID
./aidpfs recovery export --dir /absolute/private/recovery-directory \
  --id ENTRY_ID --output /absolute/new/recovered-file
```

To compare an entry with a current remote version, run:

```sh
./aidpfs recovery inspect --dir /absolute/private/recovery-directory \
  --id ENTRY_ID --region REGION --profile OCI_PROFILE
```

`inspect-create` checks the current parent listing and reports whether the target was listed as a file, directory or notebook, or was `not_listed`. This observation does not prove who created an object, whether the recorded request succeeded, or what happens after inspection. The ordinary `inspect` command generates a download PAR, compares a version-bound remote snapshot, and reports `base_version`, `uploaded_version`, `content_match_unknown_version`, or `remote_changed`. Matching content alone does not establish which client wrote it, and an uploaded version does not prove metadata completion or prevent a later change. Neither command replays a write. If the target is inaccessible or the version changes during inspection, inspection fails and the journal remains intact.

`inspect-mutation` reports whether the intent was confirmed or definitely rejected, and source and destination parent-listing observations separately. They are not an atomic snapshot and do not prove which client performed the operation. Inspect the target contents and any other relevant evidence before manual recovery; the command never repeats the mutation.

Lineage validates the selected entry and its ancestors, including their contents, target and generation. It does not establish that the selected entry is the newest: other branches or incomplete entries may exist. Avoid changing the recovery directory during inspection. Export creates a 0600 file, refuses to overwrite an existing path, and leaves the journal intact. `incomplete-or-invalid` entries cannot be exported by this command; retain their directory for manual investigation. The journal contains file data and target metadata, so protect it accordingly. Entries at `put_started` or `update_started` can represent an unknown remote outcome; inspect the remote state before taking further action. These commands do not automatically replay or delete a journal.

## Validation and development

Run `scripts/tests/run.sh` for the formatting check, `go vet`, race-enabled tests and a binary build. `scripts/test-vscode-docker.sh --build` builds the isolated Go/Node/Xvfb/FUSE runner; `scripts/test-vscode-docker.sh` runs the regular suite, and `scripts/tests/vscode/run-live.sh` runs the AIDP-specific VS Code E2E with explicit fixture settings. It uses local HTTP servers and fake backends and needs no OCI credentials or network. The mount suite exercises local FUSE, snapshot/writeback, concurrent operations, recovery, permissions and secret redaction. `scripts/tests/run-enospc.sh` creates a temporary 64 MiB tmpfs with `sudo` and verifies that a real filesystem `ENOSPC` rejects a journaled FUSE write without changing its buffer or sending a remote PUT. `scripts/tests/run.sh --live` runs the opt-in PAR/FUSE integration against an already prepared disposable `/Workspace/Shared/aidpfs-probe-<32 lowercase hex digits>` fixture; it requires explicit `AIDPFS_PROBE_*` settings, OCI credentials and `/dev/fuse` access. Live results apply only to the explicitly configured AIDP tenant and disposable fixture.

The repository includes an isolated API research script under `scripts/`; it creates and removes test data in a dedicated remote folder and requires OCI credentials. Review its behavior before running it. Keep recovery journals, tokens, PAR URLs and downloaded content private.

## License

The project is licensed under [GPL-3.0](LICENSE). Its implementation was written independently; it does not copy wsfs source code. See [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) for licenses of imported dependencies.
