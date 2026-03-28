# FSKit Backend for SSHFS on macOS

## Problem

macFUSE's default VFS backend requires a kernel extension (kext). Corporate-managed macOS machines often block kernel extensions via MDM policy, making remote filesystem mounts completely non-functional for Telepresence users on those machines.

macFUSE 5.0 introduced an optional FSKit backend (`-o backend=fskit`) that uses Apple's FSKit user-space API (macOS 15.4+) instead of a kext. SSHFS works with this backend out of the box given the correct mount option.

Telepresence currently does not pass this option, so sshfs always uses the VFS backend.

## Goal

Auto-detect macFUSE 5.x on macOS and pass `-o backend=fskit` to sshfs, enabling remote filesystem mounts without a kernel extension. Fall back to the default VFS backend on older macFUSE versions.

## FSKit Limitations

These are upstream constraints from the FSKit API (not Telepresence-specific):

- **Mount points must be under `/Volumes`.** Mounts at arbitrary paths (e.g., `/tmp/telfs-xxx`) will fail.
- **Files are always opened in read/write mode.** The `-o ro` sshfs flag may not be enforced by the backend.
- **FUSE notification API is not supported.**
- **`fuse_context_t` is not available.**
- **Most kernel-handled mount options are not implemented yet.**
- **I/O performance is slower than the kext-based VFS backend.**

## Design

### Detection

Parse the macFUSE version from `sshfs -V` stderr output. The output includes a line like `FUSE library version: ...` or `macFUSE x.y.z`. If the macFUSE major version is >= 5, the FSKit backend is available.

This detection is already partially done in `RemoteMountAvailability()` which runs `sshfs -V` and inspects the output. The version check will be added there and the result propagated to the sshfs launch code.

### SSHFS Launch (`pkg/client/remotefs/sftp.go`)

When launching sshfs on `runtime.GOOS == "darwin"` and macFUSE >= 5 is detected:

- Add `-o backend=fskit` to sshfs args.
- Remove `-o allow_root` from the args when FSKit is active — this is a kernel-handled mount option that may not be supported by the FSKit backend.

When macFUSE < 5 or not on darwin: no change to current behavior.

### Mount Point Preparation (`pkg/client/cli/mount/`)

Split `prepare_unix.go` into `prepare_linux.go` and `prepare_darwin.go` (with appropriate build tags). The linux file retains current behavior.

The darwin file:

- When FSKit backend is active and no explicit mount point is specified: create a temp directory under `/Volumes` (e.g., `/Volumes/telfs-xxx`).
- When FSKit backend is active and the user specifies a mount point outside `/Volumes`: return an error explaining the FSKit limitation.
- When FSKit backend is not active (macFUSE < 5): retain current behavior (temp dir in default location).

This requires the FSKit-detected state to be available in the mount preparation path. The `RemoteMountAvailability()` call already runs before mounts are set up. Its result (FSKit available yes/no) will be stored on the session object or passed through the context so that both mount preparation and sshfs launch can read it.

### Availability Check (`pkg/client/userd/daemon/grpc.go`)

Update `RemoteMountAvailability()`:

- Parse macFUSE major version from `sshfs -V` output.
- Continue to reject OSXFUSE (existing check).
- Accept macFUSE >= 4.0.5 (existing behavior) and macFUSE >= 5 (new).
- Store the detected version/backend capability so it can be used downstream by the sshfs launcher and mount preparation code.
- Update error messages: when sshfs is missing or macFUSE is too old, mention macFUSE 5.x with FSKit as an option for machines that cannot install kernel extensions.

### Config (`pkg/client/config.go`)

No config schema changes. The FSKit backend is auto-detected, not user-configured. The existing `MountsRoot` config option continues to work — users can set it to a `/Volumes` subdirectory if they want a specific location.

## Files Changed

| File | Change |
|------|--------|
| `pkg/client/remotefs/sftp.go` | Add `-o backend=fskit` when macFUSE >= 5 on darwin |
| `pkg/client/cli/mount/prepare_unix.go` | Rename to `prepare_linux.go`, add linux build tag |
| `pkg/client/cli/mount/prepare_darwin.go` | New file: darwin mount prep with `/Volumes` default for FSKit |
| `pkg/client/userd/daemon/grpc.go` | Parse macFUSE version, store FSKit availability, update error messages |

## Files Not Changed

- Linux, Windows, Docker, FuseFTP code paths: untouched.
- Helm chart, traffic-manager, traffic-agent: no cluster-side changes.
- Proto definitions: no RPC changes.
- Config schema: no new fields.

## Testing

- Unit test for macFUSE version parsing logic (various `sshfs -V` output formats).
- Manual testing on macOS 15.4+ with macFUSE 5.x installed.
- Verify fallback: on macOS with macFUSE 4.x, behavior is unchanged.
- Verify mount point creation under `/Volumes` and error on non-`/Volumes` paths when FSKit is active.
