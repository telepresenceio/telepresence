package rt

import (
	"os/exec"
	"runtime"
	"slices"
	"sync"
)

// Capability names an optional host capability a suite or test may require.
type Capability string

const (
	// Docker means `docker info` succeeds.
	Docker Capability = "docker"
	// Sudo means passwordless sudo is available.
	Sudo Capability = "sudo"
	// FUSE means a FUSE mount helper is present.
	FUSE Capability = "fuse"
	// Veth means the host can create veth pairs (Linux + Sudo).
	Veth Capability = "veth"
)

// capCache memoizes capability detection: each capability is probed once.
//
//nolint:gochecknoglobals // process-wide capability cache
var capCache sync.Map

// hasCapability reports whether c is available on this host, detecting it
// lazily on first use and caching the result.
func hasCapability(c Capability) bool {
	if v, ok := capCache.Load(c); ok {
		return v.(bool)
	}
	ok := detectCapability(c)
	capCache.Store(c, ok)
	return ok
}

func detectCapability(c Capability) bool {
	switch c {
	case Docker:
		return exec.Command("docker", "info").Run() == nil
	case Sudo:
		return exec.Command("sudo", "-n", "true").Run() == nil
	case FUSE:
		return detectFUSE()
	case Veth:
		return runtime.GOOS == "linux" && hasCapability(Sudo)
	default:
		return false
	}
}

func detectFUSE() bool {
	if runtime.GOOS == "darwin" {
		_, err := exec.LookPath("mount_macfuse")
		return err == nil
	}
	if _, err := exec.LookPath("fusermount3"); err == nil {
		return true
	}
	_, err := exec.LookPath("fusermount")
	return err == nil
}

// goosConstraint is the platform allow/deny list attached to a registration
// via On/NotOn.
type goosConstraint struct {
	allow []string
	deny  []string
}

// unmet returns a human-readable reason and true when the constraint is not
// satisfied by the current runtime.GOOS.
func (g goosConstraint) unmet() (string, bool) {
	if len(g.allow) > 0 && !slices.Contains(g.allow, runtime.GOOS) {
		return "requires GOOS in " + joinStrings(g.allow) + ", running on " + runtime.GOOS, true
	}
	if slices.Contains(g.deny, runtime.GOOS) {
		return "excluded on GOOS " + runtime.GOOS, true
	}
	return "", false
}

func joinStrings(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += ","
		}
		out += s
	}
	return out
}
