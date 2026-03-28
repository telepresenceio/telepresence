# FSKit SSHFS Backend Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an opt-in `useMacosFsKit` config option that tells Telepresence to pass `-o backend=fskit` to sshfs on macOS, enabling remote mounts without a kernel extension.

**Architecture:** Add a boolean config field to `Intercept`. When enabled on darwin, validate macFUSE >= 5 in `RemoteMountAvailability`, create mount points under `/Volumes`, and pass the FSKit backend flag to sshfs. All changes are darwin-only; other platforms untouched.

**Tech Stack:** Go, macFUSE 5.x, sshfs, testify

---

## File Map

| File | Action | Responsibility |
|------|--------|----------------|
| `pkg/client/config.go` | Modify | Add `UseMacosFsKit` field to `Intercept` struct |
| `pkg/client/config_test.go` | Modify | Test that `useMacosFsKit` parses from YAML config |
| `pkg/client/userd/daemon/grpc.go` | Modify | Validate macFUSE >= 5 when `UseMacosFsKit` is true |
| `pkg/client/userd/daemon/macfuse_version.go` | Create | macFUSE version parsing helper (testable) |
| `pkg/client/userd/daemon/macfuse_version_test.go` | Create | Tests for version parsing from `sshfs -V` output |
| `pkg/client/cli/mount/prepare_unix.go` | Modify | Add `!darwin` build tag |
| `pkg/client/cli/mount/prepare_darwin.go` | Create | Darwin mount prep: `/Volumes` for FSKit, else unix behavior |
| `pkg/client/remotefs/sftp.go` | Modify | Add `-o backend=fskit` and drop `-o allow_root` when config enabled on darwin |

---

### Task 1: Add `UseMacosFsKit` config field

**Files:**
- Modify: `pkg/client/config.go:766-771`
- Modify: `pkg/client/config_test.go`

- [ ] **Step 1: Write the failing test**

Add a test case to `config_test.go` that parses a config YAML with `useMacosFsKit: true` and asserts it's set on the resulting `Intercept` struct.

In the existing `TestGetConfig` function's config YAML string, add `useMacosFsKit: true` under the `intercept:` block. Then add an assertion:

```go
assert.True(t, cfg.Intercept().UseMacosFsKit)
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/client/ -run TestGetConfig -v`
Expected: compilation error — `UseMacosFsKit` does not exist on `Intercept`

- [ ] **Step 3: Add the field to the Intercept struct**

In `pkg/client/config.go`, add `UseMacosFsKit` to the `Intercept` struct:

```go
type Intercept struct {
	DefaultPort          int           `json:"defaultPort"`
	UseFtp               bool          `json:"useFtp"`
	UseMacosFsKit        bool          `json:"useMacosFsKit"`
	MountsRoot           string        `json:"mountsRoot"`
	MountCompletionDelay time.Duration `json:"mountCompletionDelay,format:units"`
}
```

No changes to `defaultIntercept` — zero value `false` is the correct default.

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/client/ -run TestGetConfig -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add pkg/client/config.go pkg/client/config_test.go
git commit -s --no-gpg-sign -m "feat: add UseMacosFsKit config field to Intercept struct"
```

---

### Task 2: macFUSE version parsing

**Files:**
- Create: `pkg/client/userd/daemon/macfuse_version.go`
- Create: `pkg/client/userd/daemon/macfuse_version_test.go`

- [ ] **Step 1: Write the failing tests**

Create `pkg/client/userd/daemon/macfuse_version_test.go`:

```go
package daemon

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseMacFUSEMajorVersion(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		expected int
		wantErr  bool
	}{
		{
			name: "macFUSE 5.x",
			output: `SSHFS version 2.10
FUSE library version: 2.9.9
macFUSE 5.0.2
fuse: no mount point`,
			expected: 5,
		},
		{
			name: "macFUSE 4.x",
			output: `SSHFS version 2.10
FUSE library version: 2.9.9
macFUSE 4.6.1
fuse: no mount point`,
			expected: 4,
		},
		{
			name: "OSXFUSE old",
			output: `SSHFS version 2.5
OSXFUSE 3.8.3
fuse: no mount point`,
			expected: 0,
			wantErr:  true,
		},
		{
			name:     "no version found",
			output:   `SSHFS version 2.10`,
			expected: 0,
			wantErr:  true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ver, err := parseMacFUSEMajorVersion([]byte(tt.output))
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, ver)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./pkg/client/userd/daemon/ -run TestParseMacFUSEMajorVersion -v`
Expected: compilation error — `parseMacFUSEMajorVersion` undefined

- [ ] **Step 3: Implement the version parser**

Create `pkg/client/userd/daemon/macfuse_version.go`:

```go
package daemon

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
)

var macFUSEVersionRe = regexp.MustCompile(`macFUSE (\d+)\.`)

// parseMacFUSEMajorVersion extracts the macFUSE major version number from
// the combined output of `sshfs -V`. Returns an error if the output contains
// OSXFUSE (too old) or no macFUSE version line is found.
func parseMacFUSEMajorVersion(output []byte) (int, error) {
	if bytes.Contains(output, []byte("OSXFUSE")) {
		return 0, fmt.Errorf("OSXFUSE detected; macFUSE 4.0.5 or higher is required")
	}
	m := macFUSEVersionRe.FindSubmatch(output)
	if m == nil {
		return 0, fmt.Errorf("macFUSE version not found in sshfs output")
	}
	ver, err := strconv.Atoi(string(m[1]))
	if err != nil {
		return 0, fmt.Errorf("failed to parse macFUSE major version: %w", err)
	}
	return ver, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./pkg/client/userd/daemon/ -run TestParseMacFUSEMajorVersion -v`
Expected: PASS (all 4 cases)

- [ ] **Step 5: Commit**

```bash
git add pkg/client/userd/daemon/macfuse_version.go pkg/client/userd/daemon/macfuse_version_test.go
git commit -s --no-gpg-sign -m "feat: add macFUSE version parser for FSKit detection"
```

---

### Task 3: Update `RemoteMountAvailability` to validate FSKit

**Files:**
- Modify: `pkg/client/userd/daemon/grpc.go:385-417`

- [ ] **Step 1: Update RemoteMountAvailability**

Replace the existing `RemoteMountAvailability` method body (lines 385-417) with logic that handles the `UseMacosFsKit` config. The method already runs `sshfs -V` and checks the output. Add a macFUSE >= 5 check when `UseMacosFsKit` is true.

```go
func (s *service) RemoteMountAvailability(ctx context.Context, ex *empty.Empty) (*empty.Empty, error) {
	if proc.RunningInContainer() {
		// We mount using docker volumes and the telemount driver plugin.
		return ex, nil
	}
	if client.GetConfig(ctx).Intercept().UseFtp {
		return ex, s.FuseFTPError()
	}

	// Use CombinedOutput to include stderr which has information about whether they
	// need to upgrade to a newer version of macFUSE or not
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = proc.CommandContext(ctx, "sshfs-win", "cmd", "-V")
	} else {
		cmd = proc.CommandContext(ctx, "sshfs", "-V")
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		clog.Errorf(ctx, "sshfs not installed: %v", err)
		return ex, errcat.User.New("sshfs is not installed on your local machine")
	}

	if runtime.GOOS == "darwin" && client.GetConfig(ctx).Intercept().UseMacosFsKit {
		ver, verErr := parseMacFUSEMajorVersion(out)
		if verErr != nil {
			return ex, errcat.User.Newf("useMacosFsKit is enabled but %v", verErr)
		}
		if ver < 5 {
			return ex, errcat.User.Newf("useMacosFsKit requires macFUSE 5.0 or higher, but macFUSE %d.x is installed", ver)
		}
		return ex, nil
	}

	// OSXFUSE changed to macFUSE, and we've noticed that older versions of OSXFUSE
	// can cause browsers to hang + kernel crashes, so we add an error to prevent
	// our users from running into this problem.
	if bytes.Contains(out, []byte("OSXFUSE")) {
		return ex, errcat.User.New(`macFUSE 4.0.5 or higher is required on your local machine`)
	}
	return ex, nil
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./pkg/client/userd/daemon/`
Expected: success

- [ ] **Step 3: Commit**

```bash
git add pkg/client/userd/daemon/grpc.go
git commit -s --no-gpg-sign -m "feat: validate macFUSE >= 5 when useMacosFsKit is enabled"
```

---

### Task 4: Darwin mount point preparation

**Files:**
- Modify: `pkg/client/cli/mount/prepare_unix.go` (add build tag)
- Create: `pkg/client/cli/mount/prepare_darwin.go`

- [ ] **Step 1: Add `!darwin` build tag to `prepare_unix.go`**

Change the build tag from:
```go
//go:build !windows
```
to:
```go
//go:build !windows && !darwin
```

- [ ] **Step 2: Create `prepare_darwin.go`**

```go
//go:build darwin

package mount

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

func prepare(ctx context.Context, cwd string, mountPoint string) (string, error) {
	cfg := client.GetConfig(ctx).Intercept()
	if !cfg.UseMacosFsKit {
		return prepareUnix(ctx, cwd, mountPoint)
	}

	if mountPoint == "" {
		root := cfg.MountsRoot
		if root == "" {
			root = "/Volumes"
		}
		return os.MkdirTemp(root, "telfs-")
	}

	// filepath.Abs uses os.Getwd but we need the working dir of the cli
	if !filepath.IsAbs(mountPoint) {
		mountPoint = filepath.Join(cwd, mountPoint)
		mountPoint = filepath.Clean(mountPoint)
	}

	if !strings.HasPrefix(mountPoint, "/Volumes/") {
		return "", fmt.Errorf(
			"the FSKit backend requires mount points under /Volumes, but %q was specified. "+
				"Either use a path under /Volumes or disable useMacosFsKit in your Telepresence config", mountPoint)
	}

	return mountPoint, os.MkdirAll(mountPoint, 0o700)
}

// prepareUnix is the default unix behavior, used when FSKit is not active.
func prepareUnix(ctx context.Context, cwd string, mountPoint string) (string, error) {
	if mountPoint == "" {
		return os.MkdirTemp(client.GetConfig(ctx).Intercept().MountsRoot, "telfs-")
	}

	// filepath.Abs uses os.Getwd but we need the working dir of the cli
	if !filepath.IsAbs(mountPoint) {
		mountPoint = filepath.Join(cwd, mountPoint)
		mountPoint = filepath.Clean(mountPoint)
	}

	return mountPoint, os.MkdirAll(mountPoint, 0o700)
}
```

- [ ] **Step 3: Verify it compiles**

Run: `go build ./pkg/client/cli/mount/`
Expected: success

- [ ] **Step 4: Commit**

```bash
git add pkg/client/cli/mount/prepare_unix.go pkg/client/cli/mount/prepare_darwin.go
git commit -s --no-gpg-sign -m "feat: darwin mount prep uses /Volumes for FSKit backend"
```

---

### Task 5: Pass `-o backend=fskit` to sshfs

**Files:**
- Modify: `pkg/client/remotefs/sftp.go:69-83`

- [ ] **Step 1: Update sshfs args construction**

In `sftp.go`, inside the `backoff.Retry` callback (around line 69), after the base sshfs args are constructed, add the FSKit backend flag when enabled. The `Start` method receives a `ctx` that has the client config available.

Replace the sshfs args block (lines 69-83) with:

```go
			sshfsArgs := []string{
				"-F", "none", // don't load the user's config file
				"-f", // foreground operation

				// connection settings
				"-C", // compression
				"-oConnectTimeout=10",

				// mount directives
				"-o", "follow_symlinks",
			}

			useFsKit := runtime.GOOS == "darwin" && client.GetConfig(ctx).Intercept().UseMacosFsKit
			if !useFsKit {
				// allow_root is a kernel mount option not supported by the FSKit backend
				sshfsArgs = append(sshfsArgs, "-o", "allow_root")
			} else {
				sshfsArgs = append(sshfsArgs, "-o", "backend=fskit")
			}

			if ro {
				sshfsArgs = append(sshfsArgs, "-o", "ro")
			}
```

- [ ] **Step 2: Add the missing import**

Add `"github.com/telepresenceio/telepresence/v2/pkg/client"` to the imports in `sftp.go`.

- [ ] **Step 3: Verify it compiles**

Run: `go build ./pkg/client/remotefs/`
Expected: success

- [ ] **Step 4: Commit**

```bash
git add pkg/client/remotefs/sftp.go
git commit -s --no-gpg-sign -m "feat: pass -o backend=fskit to sshfs when useMacosFsKit is enabled"
```

---

### Task 6: End-to-end verification

- [ ] **Step 1: Run unit tests**

Run: `go test ./pkg/client/ ./pkg/client/userd/daemon/ -v`
Expected: all tests pass

- [ ] **Step 2: Run the linter**

Run: `make lint-go`
Expected: no new lint errors

- [ ] **Step 3: Build the binary**

Run: `make build`
Expected: success

- [ ] **Step 4: Final commit (if any lint/build fixes needed)**

```bash
git add -u
git commit -s --no-gpg-sign -m "fix: address lint issues for FSKit backend"
```
