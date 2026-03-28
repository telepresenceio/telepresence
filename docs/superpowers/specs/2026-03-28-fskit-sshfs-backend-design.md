# FSKit Backend for SSHFS on macOS

## Problem

macFUSE's default VFS backend requires a kernel extension (kext). Corporate-managed macOS machines often block kernel extensions via MDM policy, making remote filesystem mounts completely non-functional for Telepresence users on those machines.

macFUSE 5.0 introduced an optional FSKit backend (`-o backend=fskit`) that uses Apple's FSKit user-space API (macOS 15.4+) instead of a kext. SSHFS works with this backend out of the box given the correct mount option.

Telepresence currently does not pass this option, so sshfs always uses the VFS backend.

## Goal

Add an opt-in config option to use the macFUSE FSKit backend on macOS. When enabled and macFUSE 5.x is detected, pass `-o backend=fskit` to sshfs. When disabled (default), behavior is unchanged.

## FSKit Limitations

These are upstream constraints from the FSKit API (not Telepresence-specific):

- **Mount points must be under `/Volumes`.** Mounts at arbitrary paths (e.g., `/tmp/telfs-xxx`) will fail.
- **Files are always opened in read/write mode.** The `-o ro` sshfs flag may not be enforced by the backend.
- **FUSE notification API is not supported.**
- **`fuse_context_t` is not available.**
- **Most kernel-handled mount options are not implemented yet.**
- **I/O performance is slower than the kext-based VFS backend.**

## Design

### Config Option (`pkg/client/config.go`)

Add a new boolean field `UseMacosFsKit` to the `Intercept` struct:

```go
type Intercept struct {
    DefaultPort          int           `json:"defaultPort"`
    UseFtp               bool          `json:"useFtp"`
    UseMacosFsKit             bool          `json:"useMacosFsKit"`
    MountsRoot           string        `json:"mountsRoot"`
    MountCompletionDelay time.Duration `json:"mountCompletionDelay,format:units"`
}
```

Default: `false`. Users opt in via their Telepresence config file:

```yaml
intercept:
  useMacosFsKit: true
```

When `useMacosFsKit` is `true`, Telepresence will attempt to use the FSKit backend. If macFUSE >= 5 is not detected, `RemoteMountAvailability()` returns an error telling the user they need macFUSE 5.x.

### Detection

Parse the macFUSE version from `sshfs -V` stderr output. The output includes a line like `macFUSE x.y.z`. If the macFUSE major version is >= 5, the FSKit backend is available.

This detection is already partially done in `RemoteMountAvailability()` which runs `sshfs -V` and inspects the output. The version check will be added there.

Detection only runs when `useMacosFsKit` is `true` on darwin. On other platforms or when `useMacosFsKit` is `false`, the existing code path is unchanged.

### SSHFS Launch (`pkg/client/remotefs/sftp.go`)

When `useMacosFsKit` is enabled and macFUSE >= 5 is confirmed:

- Add `-o backend=fskit` to sshfs args.
- Remove `-o allow_root` from the args — this is a kernel-handled mount option that may not be supported by the FSKit backend.

Otherwise: no change to current behavior.

### Mount Point Preparation (`pkg/client/cli/mount/`)

Keep `prepare_unix.go` as-is. Add `prepare_darwin.go` with a `//go:build darwin` tag that overrides the unix `prepare` function on macOS.

The darwin file:

- When FSKit mode is active and no explicit mount point is specified: create a temp directory under `/Volumes` (e.g., `/Volumes/telfs-xxx`).
- When FSKit mode is active and the user specifies a mount point outside `/Volumes`: return an error explaining the FSKit limitation.
- When FSKit mode is not active: delegate to the same logic as `prepare_unix.go`.

The `useMacosFsKit` config value is already available via `client.GetConfig(ctx)`.

### Availability Check (`pkg/client/userd/daemon/grpc.go`)

Update `RemoteMountAvailability()`:

- When `useMacosFsKit` is `true` on darwin: parse macFUSE major version from `sshfs -V` output. If < 5, return an error saying macFUSE 5.x is required for FSKit mode.
- Continue to reject OSXFUSE (existing check).
- Accept macFUSE >= 4.0.5 (existing behavior) when `useMacosFsKit` is `false`.
- Update error messages to mention FSKit as an option for machines that cannot install kernel extensions.

## Files Changed

| File | Change |
|------|--------|
| `pkg/client/config.go` | Add `UseMacosFsKit` bool to `Intercept` struct |
| `pkg/client/remotefs/sftp.go` | Add `-o backend=fskit` when `useMacosFsKit` enabled on darwin |
| `pkg/client/cli/mount/prepare_unix.go` | Add `!darwin` build tag so darwin uses its own file |
| `pkg/client/cli/mount/prepare_darwin.go` | New file: darwin mount prep with `/Volumes` default for FSKit, falls back to unix logic otherwise |
| `pkg/client/userd/daemon/grpc.go` | Validate macFUSE >= 5 when `useMacosFsKit` is true, update error messages |

## Files Not Changed

- Linux, Windows, Docker, FuseFTP code paths: untouched.
- Helm chart, traffic-manager, traffic-agent: no cluster-side changes.
- Proto definitions: no RPC changes.

## Testing

- Unit test for macFUSE version parsing logic (various `sshfs -V` output formats).
- Manual testing on macOS 15.4+ with macFUSE 5.x installed.
- Verify fallback: on macOS with macFUSE 4.x, behavior is unchanged.
- Verify mount point creation under `/Volumes` and error on non-`/Volumes` paths when FSKit is active.
