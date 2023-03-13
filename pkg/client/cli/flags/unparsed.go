package flags

import (
	"fmt"
	"strings"
)

// GetUnparsedValue returns the value of a flag that has been provided after a "--" on the command
// line, and hence hasn't been parsed as a normal flag. Typical use case is:
//
//	telepresence intercept --docker-run ... -- --name <name>
func GetUnparsedValue(args []string, flag string) (string, error) {
	feq := flag + "="
	for i, arg := range args {
		var v string
		switch {
		case arg == flag:
			i++
			if i < len(args) {
				if v = args[i]; strings.HasPrefix(v, "-") {
					v = ""
				}
			}
		case strings.HasPrefix(arg, feq):
			v = arg[len(feq):]
		default:
			continue
		}
		if v == "" {
			return "", fmt.Errorf("flag %q requires a value", flag)
		}
		return v, nil
	}
	return "", nil
}
