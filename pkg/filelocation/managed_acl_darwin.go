package filelocation

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
)

var aclEntry = regexp.MustCompile(`^\s*\d+: .* (allow|deny) (\S+)$`)

// writeRights are the rights in an access control list entry that let a user change the file or the directory
// entries below it, or change who may.
var writeRights = map[string]bool{ //nolint:gochecknoglobals // constant
	"write": true, "append": true, "delete": true, "delete_child": true, "add_file": true, "add_subdirectory": true,
	"writeattr": true, "writeextattr": true, "writesecurity": true, "chown": true,
}

// aclWriter returns a description of the first entry in the access control list of path that allows
// a change, or "" when there is none.
func aclWriter(path string) (string, error) {
	out, err := exec.Command("/bin/ls", "-ldeP", path).Output()
	if err != nil {
		return "", fmt.Errorf("list access control entries of %s: %w", path, err)
	}
	for _, line := range strings.Split(string(out), "\n")[1:] {
		m := aclEntry.FindStringSubmatch(line)
		if m == nil || m[1] != "allow" {
			continue
		}
		for _, r := range strings.Split(m[2], ",") {
			if writeRights[r] {
				return strings.TrimSpace(line), nil
			}
		}
	}
	return "", nil
}
