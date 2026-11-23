package dns

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/telepresenceio/clog"
)

const maxDNSNameLength = 253

// ValidName reports whether name is a DNS name that is safe to use as a domain, a suffix, or as part
// of a file name: dot-separated labels of letters, digits, hyphen and underscore, with no empty
// labels, at most 253 characters long, and with an optional trailing dot. When allowLeadingDot is
// true, a single leading dot is accepted too.
func ValidName(name string, allowLeadingDot bool) bool {
	if allowLeadingDot {
		name = strings.TrimPrefix(name, ".")
	}
	name = strings.TrimSuffix(name, ".")
	if name == "" || len(name) > maxDNSNameLength {
		return false
	}
	labelLen := 0
	for i := 0; i < len(name); i++ {
		switch c := name[i]; {
		case c == '.':
			if labelLen == 0 {
				return false
			}
			labelLen = 0
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			labelLen++
			if labelLen > 63 {
				return false
			}
		default:
			return false
		}
	}
	return labelLen > 0
}

// ValidNames returns the names that are valid according to ValidName and logs a warning for
// each one that is not.
func ValidNames(c context.Context, what string, names []string, allowLeadingDot bool) []string {
	if len(names) == 0 {
		return names
	}
	valid := make([]string, 0, len(names))
	for _, name := range names {
		if ValidName(name, allowLeadingDot) {
			valid = append(valid, name)
		} else {
			clog.Warnf(c, "Ignoring invalid DNS %s %q", what, name)
		}
	}
	return valid
}

// resolverFilePath returns the path of the resolver file for domain, or an error unless the path is
// directly inside resolverDir.
func resolverFilePath(resolverDir, domain string) (string, error) {
	p := filepath.Join(resolverDir, "telepresence."+domain)
	if filepath.Dir(p) != filepath.Clean(resolverDir) {
		return "", fmt.Errorf("resolver file for domain %q is outside %s", domain, resolverDir)
	}
	return p, nil
}
