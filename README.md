# aidpfs

`aidpfs` is an experimental Linux FUSE filesystem for reading one Oracle AI Data Platform (AIDP) Workbench workspace subtree. It uses the WorkspaceObject and notebook contents APIs with OCI API-key authentication. It is under development; see [TASK.md](TASK.md) for outstanding work.

**The current mount is read-only.** File creation, editing, saving, renaming, and deletion are not supported. In a tested AIDP environment, both contents and WorkspaceObject updates accepted an incorrect `if-match` and changed data; do not rely on those APIs for conflict protection without independently verifying your environment.

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

Unmount in a second terminal with `fusermount3 -u "$HOME/aidp-mount"`, or stop the foreground process with Ctrl-C. Verify the directory is no longer mounted before removing it. If unmount fails, close processes using the mount and retry; avoid force-unmounting an unrelated filesystem.

## Current behavior and limitations

- Mount access is restricted to the mounting user; directories are mode 0500 and files 0400. `allow_other`, volumes and multiple workspaces are not supported.
- Files are fetched in full on open and held in memory for that open. Each open gets a snapshot; a later open fetches again. Metadata and negative lookup TTLs are zero and direct I/O bypasses the kernel page cache. Concurrent changes between separate API operations can still produce inconsistent results.
- `stat` fetches file contents to report the size actually returned. API responses larger than 64 MiB fail with `EFBIG`, so binary file size limits depend on the encoded response. This approach can use substantial memory and API requests.
- Notebooks are exposed as the JSON content returned by the contents API. They may not have the same byte representation as the original stored notebook. In one synthetic API test, cell representation and metadata changed and the PAR JSON differed from contents JSON. Saving and full round-trip preservation remain unverified.
- Unsupported object types cause directory listing to fail, rather than being silently hidden. The `objects` and contents APIs do not have identical path behavior; only the tested `/Workspace` subtree is known to work. Directory listing, permissions, and paths may differ across AIDP deployments.
- GET requests alone are retried for selected transient failures. No upload/download PAR workflow is used by the filesystem. Workbench UI, editors, Git operations, multi-user access and large-file behavior are not yet verified end to end.

## Recovery journal inspection

The experimental save coordinator can retain local recovery entries, but it is not connected to the current read-only FUSE mount. For an existing private recovery directory, inspect entries and export verified journal contents to a **new** local file:

```sh
./aidpfs recovery list --dir /absolute/private/recovery-directory
./aidpfs recovery export --dir /absolute/private/recovery-directory \
  --id ENTRY_ID --output /absolute/new/recovered-file
```

To compare an entry with a current remote version, run:

```sh
./aidpfs recovery inspect --dir /absolute/private/recovery-directory \
  --id ENTRY_ID --region REGION --profile OCI_PROFILE
```

The command generates a download PAR, compares a version-bound remote snapshot, and reports `base_version`, `uploaded_version`, `content_match_unknown_version`, or `remote_changed`. This is a point-in-time observation: matching content alone does not establish which client wrote it, and an uploaded version does not prove metadata completion or prevent a later change. It does not replay a write. If the target is missing, inaccessible, or the version changes during inspection, inspection fails and the journal remains intact.

Export creates a 0600 file, refuses to overwrite an existing path, and leaves the journal intact. `incomplete-or-invalid` entries cannot be exported by this command; retain their directory for manual investigation. The journal contains file data and target metadata, so protect it accordingly. Entries at `put_started` or `update_started` can represent an unknown remote outcome; inspect the remote state before taking further action. These commands do not automatically replay or delete a journal.

## Validation and development

Once Go module dependencies are available locally, `make test` runs without OCI credentials or external network access; tests use local HTTP servers and a fake backend. A FUSE mount test is skipped if the device or mount permissions are unavailable. `go test -race ./...` and `go vet ./...` provide additional checks. Some integrations require an actual AIDP workspace and FUSE-capable host; successful local tests do not prove those integrations.

The repository includes an isolated API research script under `scripts/`; it creates and removes test data in a dedicated remote folder and requires OCI credentials. Review its behavior before running it. Keep recovery journals, tokens, PAR URLs and downloaded content private.

## License

The project is licensed under [GPL-3.0](LICENSE). Its implementation was written independently; it does not copy wsfs source code. Dependencies are listed in `go.mod` and retain their own licenses, including go-fuse and the Oracle OCI Go SDK.
