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
