//go:build linux

package procfs

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Environ returns the environment of pid, read from /proc/<pid>/environ.
// Kernel threads and processes that have exited their exec have an empty
// environ; Environ returns an empty, non-nil map for those, not an error.
func Environ(pid int) (map[string]string, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/environ", pid))
	if err != nil {
		return nil, fmt.Errorf("read environ of pid %d: %w", pid, err)
	}
	env := make(map[string]string)
	for entry := range strings.SplitSeq(string(raw), "\x00") {
		if entry == "" {
			continue
		}
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		env[key] = value
	}
	return env, nil
}

// RootPath returns /proc/<pid>/root joined with elems. The kernel resolves
// this magic symlink in the target process's mount namespace, so reads
// through the returned path see the target's filesystem exactly as the
// target sees it -- without the caller entering that mount namespace
// (no setns required).
func RootPath(pid int, elems ...string) string {
	return filepath.Join(append([]string{fmt.Sprintf("/proc/%d/root", pid)}, elems...)...)
}

// IDMapEntry is one line of a /proc/<pid>/uid_map or gid_map file: Length
// consecutive IDs starting at OutsideID in the namespace that owns pid map
// to IDs starting at InsideID inside pid's user namespace.
type IDMapEntry struct {
	InsideID  uint64
	OutsideID uint64
	Length    uint64
}

// UIDMap parses /proc/<pid>/uid_map: whitespace-separated
// "InsideID OutsideID Length" triples, one per line.
func UIDMap(pid int) ([]IDMapEntry, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/uid_map", pid))
	if err != nil {
		return nil, fmt.Errorf("read uid_map of pid %d: %w", pid, err)
	}
	var entries []IDMapEntry
	for line := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) != 3 {
			return nil, fmt.Errorf("uid_map of pid %d: malformed line %q", pid, line)
		}
		var vals [3]uint64
		for i, f := range fields {
			v, err := strconv.ParseUint(f, 10, 64)
			if err != nil {
				return nil, fmt.Errorf("uid_map of pid %d: malformed line %q: %w", pid, line, err)
			}
			vals[i] = v
		}
		entries = append(entries, IDMapEntry{InsideID: vals[0], OutsideID: vals[1], Length: vals[2]})
	}
	return entries, nil
}

// InUserNamespace reports whether pid runs in a user namespace other than
// the init user namespace. The init user namespace's uid_map is always
// exactly the single identity entry "0 0 4294967295"; any other map
// (including a differently-shaped identity map) means pid was unshared into
// a non-init user namespace.
//
// This matters when installing netfilter rules into pid's network namespace
// from outside: a socket-owner (skgid/skuid) match can only be installed
// when that namespace is owned by the init user namespace -- the kernel
// rejects it with EINVAL otherwise -- so a non-init owner forces a
// packet-mark discriminator instead.
func InUserNamespace(pid int) (bool, error) {
	entries, err := UIDMap(pid)
	if err != nil {
		return false, err
	}
	if len(entries) == 1 {
		e := entries[0]
		if e.InsideID == 0 && e.OutsideID == 0 && e.Length == 4294967295 {
			return false, nil
		}
	}
	return true, nil
}
