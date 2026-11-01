# aidpfs

`aidpfs` is an experimental Linux FUSE filesystem for reading one Oracle AI Data Platform (AIDP) Workbench workspace subtree. It uses the WorkspaceObject and notebook contents APIs with OCI API-key authentication. It is under development; see [TASK.md](TASK.md) for outstanding work.

**The default mount is read-only.** An explicitly enabled experimental mode can create directories and create or modify regular files, and is intended only for isolated test data. Renaming, deletion and notebook saving are not supported. In a tested AIDP environment, both contents and WorkspaceObject updates accepted an incorrect `if-match` and changed data; do not rely on those APIs for conflict protection without independently verifying your environment.

## Requirements

- Linux, Go 1.26 or newer, `/dev/fuse`, and `fusermount3`; the user must be able to mount and unmount FUSE filesystems.
- An OCI API-key profile, permission to list workspace objects and read notebook contents, and network access to the AIDP endpoint. Other authentication methods have not been validated.
- The platform OCID and the **workspace key** returned by the workspace API. The key is not necessarily the workspace display name.
- An existing, empty local mount directory owned by the current user. The examples use `/Workspace`; select a subtree available to your identity. The `/` remote root is currently unsupported because its API path mapping is not established.

## Build and mount

```sh
make test
make build
mkdir -p "$HOME/aidp-mount"
./aidpfs --profile OCI_PROFILE --region REGION \
  --platform-id PLATFORM_OCID --workspace-key WORKSPACE_KEY \
  --remote-path /Workspace --mount-point "$HOME/aidp-mount"
```

The process stays in the foreground. The endpoint defaults to `https://aidp.<region>.oci.oraclecloud.com`; use `--endpoint` for a different HTTPS AIDP origin. OCI configuration defaults to `~/.oci/config`; set `OCI_CONFIG_FILE` to choose another file. Credentials belong in OCI's configuration and private key files, not command arguments or logs. Use `--version` for the build version and `--help` for flags.

Unmount in a second terminal with `fusermount3 -u "$HOME/aidp-mount"`, or stop the foreground process with Ctrl-C. Verify the directory is no longer mounted before removing it. If signal-triggered unmount fails, the process reports the error and remains running; close processes using the mount and send Ctrl-C again or retry unmounting externally. Avoid force-unmounting an unrelated filesystem.

## Current behavior and limitations

- Mount access is restricted to the mounting user; directories are mode 0500 and files 0400. `allow_other`, volumes and multiple workspaces are not supported.
- Read-only file and directory ownership uses the mounting UID/GID, and a nonzero remote modification time is exposed as `mtime`. `chmod`, `chown`, timestamp changes, file writes and creation are rejected with `EROFS` on the read-only mount.
- `statfs` returns `EOPNOTSUPP`: the AIDP workspace API does not provide filesystem capacity or quota, and reporting zero-valued capacity fields would be misleading.
- Files are fetched in full on open and held in memory for that open. Each open gets a snapshot; a later open fetches again. Metadata and negative lookup TTLs are zero and direct I/O bypasses the kernel page cache. Concurrent changes between separate API operations can still produce inconsistent results.
- `stat` fetches file contents to report the size actually returned. API responses larger than 64 MiB fail with `EFBIG`, so binary file size limits depend on the encoded response. This approach can use substantial memory and API requests.
- Notebooks are exposed as the JSON content returned by the contents API. They may not have the same byte representation as the original stored notebook. Synthetic REST tests showed first-save normalization in cell source representation and metadata, an output structure difference, and PAR JSON differing from contents JSON; a subsequent read-save-read of the normalized response was stable. Workbench UI behavior and preservation of existing notebooks remain unverified.
- In the tested tenancy, contents API creation accepted ASCII and space in names but returned `400 InvalidParameter` for Japanese characters, `%`, and `#`. This is an observation for that write route and encoding, not a claim about reading existing names or other AIDP APIs.
- Unsupported object types cause directory listing to fail, rather than being silently hidden. The `objects` and contents APIs do not have identical path behavior; only the tested `/Workspace` subtree is known to work. Directory listing, permissions, and paths may differ across AIDP deployments.
- GET requests alone are retried for selected transient failures. The default mount does not use PAR. Workbench UI editing, multi-user access, and large-file behavior are not yet verified end to end; the limited Vim and Git checks described below do not cover ordinary editor settings or full Git workflows.

## Experimental file writes

Use only a dedicated test subtree containing disposable regular files. Create a private, empty journal directory and add `--experimental-write --recovery-dir /absolute/private/directory` to the mount command. Both options must be supplied together. A warning is printed at startup; the read-only mode remains the default.

This mode is incomplete: it cannot rename or delete files or save notebooks, and many editors rely on those operations. File and directory creation use non-overwriting requests, but if a request fails or a subsequent file open fails the object may still exist; inspect the target before retrying. A durable create intent records the target before the request; `recovery list` shows intents and whether the server returned success. An unconfirmed intent does not establish whether creation occurred, and a confirmed intent does not establish that the object still exists or that opening it succeeded. Directories are exposed as 0700 and files as 0600 regardless of requested mode. The mount holds files in memory, limits them to 64 MiB and writes a full local journal snapshot for each mutation; storage usage can grow quickly. Writes use conditional Object Storage PAR operations and post-write verification, but these do not provide a transaction across all AIDP clients and metadata operations. Concurrent clients, multiple mounts, crashes, shutdown, disk exhaustion, and Workbench editing are not fully validated. A failed or uncertain save freezes the affected buffer and requires manual recovery; journal entries are not automatically replayed or removed. Startup rejects a nonempty recovery directory and locks it against another cooperating mount while running. Inspect retained entries and use a new empty directory for another run; the directory lock does not prevent another mount with a different journal from accessing the same remote files. Use `fsync` and check its result; a successful close is not a complete recovery or durability guarantee.

The experimental operation contract is deliberately limited:

| Operation | Result | Meaning |
|---|---|---|
| Create regular file / mkdir | Supported, no overwrite | A durable local intent precedes one remote request. A later open failure may leave the remote object present; inspect before retrying. |
| Open existing regular file for write; write/truncate | Supported | Edits stay in a bounded in-memory buffer and local journal. Notebook writes are unsupported. |
| `fsync` / synchronous write | Supported | Returns success only after conditional PAR transfer, metadata completion, and version/content verification. It does not prevent a later external update. |
| `flush` | May be called more than once | Each writable flush attempts the same save path and returns its error. |
| `release` / close | Releases local handle only | Does not start network I/O; close cannot report a save failure. Check `fsync` explicitly. |
| `unlink`, `rmdir`, `rename` | `EOPNOTSUPP` | AIDP preconditions and deletion/move atomicity are not safe enough to expose. |
| chmod, chown, timestamp changes | No-op or unsupported | AIDP does not expose these POSIX metadata operations. |
| POSIX advisory locks | Kernel-local only | A local lock may succeed on one mount, but it is not sent to AIDP and does not coordinate with Workbench, other mounts, or clients. |
| `statfs` | `EOPNOTSUPP` | AIDP capacity and quota are unavailable. |

On a failed or uncertain `fsync`, the dirty buffer is frozen and its journal remains for manual inspection. `ESTALE` indicates a detected version conflict; other outcome-unknown failures also require reconciliation. A verified save means the expected bytes were observed at the saved version during verification, not that other clients are locked out.

The experimental mount uses the write offset supplied by the kernel, including after `fcntl` changes to `O_APPEND`. Two-handle append and flag changes were tested on the current Linux FUSE environment; behavior across kernels and larger concurrent workloads remains unverified. Atomic append across mounts or other AIDP clients is not provided.

The experimental mount reuses a live FUSE inode for repeated lookups of the same path and type. This inode is not a verified AIDP object identity: an external delete and recreation may not be distinguishable. A configured noninteractive Vim save has been tested on a synthetic file; ordinary Vim settings, VS Code, and Workbench editing remain unverified.

For Git experiments, keep repository metadata on a local filesystem and specify it with `--git-dir` together with `--work-tree` pointing at the mount. A real AIDP synthetic-file test covered local repository initialization, `status`, `add`, `commit`, and `rg`; searching one file took about 4.1 seconds. It did not test a repository inside the mount, checkout, merge, reset, or operations that require rename and deletion. Search and Git operations may issue many remote requests.

After the FUSE server stops, the experimental CLI rejects new state-changing operations, waits for already entered operations, and reports an error if it observes dirty buffers, invalid or unresolved snapshot records, or unconfirmed/invalid create intents. Prepared ancestors of a validated verified snapshot are treated as completed history; separate branches remain unresolved. Draining and the scan can take time; a stuck operation can delay exit. This check does not replace checking each write, `fsync` and close result, and abrupt termination or real network failures during shutdown have not been fully validated.

Experimental `unlink`, `rmdir` and `rename` return `EOPNOTSUPP`. In the tested AIDP environment, both contents and WorkspaceObject DELETE removed a synthetic file despite an incorrect `if-match`; contents DELETE also removed a nonempty synthetic directory and its child. Rename accepted an incorrect `if-match`, and one concurrent rename experiment timed out. These observations do not establish safe conditional deletion or rename.

## Recovery journal inspection

The experimental write mount retains local recovery entries; the default read-only mount does not. For an existing private recovery directory, inspect entries and export verified journal contents to a **new** local file:

```sh
./aidpfs recovery list --dir /absolute/private/recovery-directory
./aidpfs recovery inspect-create --dir /absolute/private/recovery-directory \
  --id CREATE_INTENT_ID --region REGION --profile OCI_PROFILE
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

Lineage validates the selected entry and its ancestors, including their contents, target and generation. It does not establish that the selected entry is the newest: other branches or incomplete entries may exist. Avoid changing the recovery directory during inspection. Export creates a 0600 file, refuses to overwrite an existing path, and leaves the journal intact. `incomplete-or-invalid` entries cannot be exported by this command; retain their directory for manual investigation. The journal contains file data and target metadata, so protect it accordingly. Entries at `put_started` or `update_started` can represent an unknown remote outcome; inspect the remote state before taking further action. These commands do not automatically replay or delete a journal.

## Validation and development

Run `scripts/tests/run.sh` for the formatting check, `go vet`, race-enabled tests and a binary build. It uses local HTTP servers and fake backends and needs no OCI credentials or network. The mount suite exercises local FUSE, snapshot/writeback, concurrent operations, recovery, permissions and secret redaction. `scripts/tests/run.sh --live` runs the opt-in PAR/FUSE integration against an already prepared disposable `/Workspace/Shared/aidpfs-probe-<32 lowercase hex digits>` fixture; it requires explicit `AIDPFS_PROBE_*` settings, OCI credentials and `/dev/fuse` access. AIDP and Workbench behavior still require their own live checks.

The repository includes an isolated API research script under `scripts/`; it creates and removes test data in a dedicated remote folder and requires OCI credentials. Review its behavior before running it. Keep recovery journals, tokens, PAR URLs and downloaded content private.

## License

The project is licensed under [GPL-3.0](LICENSE). Its implementation was written independently; it does not copy wsfs source code. See [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md) for licenses of imported dependencies.
