package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// maxResolveSteps bounds resolveInRoot's work, matching os/root.go's own symlink-loop limit.
const maxResolveSteps = 255

// resolveInRoot resolves rel inside rootDir the way a process chrooted to rootDir would: relative
// symlinks are followed in place, absolute ones restart from rootDir, and ".." never rises above it.
// The result is returned relative to rootDir, without a leading slash ("" for rootDir itself).
func resolveInRoot(rootDir, rel string) (string, error) {
	pending := splitPathComponents(rel)
	cur := ""
	steps := 0
	for len(pending) > 0 {
		steps++
		if steps > maxResolveSteps {
			return "", fmt.Errorf("resolveInRoot: too many steps resolving %q under %q", rel, rootDir)
		}
		comp := pending[0]
		pending = pending[1:]
		if comp == "." {
			continue
		}
		if comp == ".." {
			cur = parentPathComponent(cur)
			continue
		}
		next := filepath.Join(cur, comp)
		fi, err := os.Lstat(filepath.Join(rootDir, next))
		if err != nil {
			return "", err
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			cur = next
			continue
		}
		target, err := os.Readlink(filepath.Join(rootDir, next))
		if err != nil {
			return "", err
		}
		if filepath.IsAbs(target) {
			cur = ""
		}
		pending = append(splitPathComponents(target), pending...)
	}
	return cur, nil
}

// splitPathComponents splits p on "/" and drops empty elements, so a leading, trailing, or
// doubled separator never produces an empty path component.
func splitPathComponents(p string) []string {
	parts := strings.Split(filepath.ToSlash(p), "/")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// parentPathComponent returns cur's parent, lexically, without rising above "" (the root).
func parentPathComponent(cur string) string {
	if cur == "" {
		return ""
	}
	dir := filepath.Dir(cur)
	if dir == "." {
		return ""
	}
	return dir
}
