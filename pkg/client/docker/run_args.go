package docker

import "strings"

var runBoolFlags = map[string]bool{ //nolint:gochecknoglobals // this is a constant
	"--detach":                true,
	"--disable-content-trust": true,
	"--help":                  true,
	"--init":                  true,
	"--interactive":           true,
	"--no-healthcheck":        true,
	"--oom-kill-disable":      true,
	"--privileged":            true,
	"--publish-all":           true,
	"--quiet":                 true,
	"--read-only":             true,
	"--rm":                    true,
	"--sig-proxy":             true,
	"--tty":                   true,
	"--use-api-socket":        true,
}

// Shorthand flags of docker run that require a value.
const runValueShorthands = "acehlmpuvw"

// ImageArg returns the image of the given docker run arguments, i.e. the first argument
// that isn't an option or an option value, together with its index. The index is -1 when
// no image is found. This requires knowledge about boolean docker flags, and if new such
// flags arrive and are used, this function might return an incorrect image.
func ImageArg(args []string) (string, int) {
	t := len(args)
	for i := 0; i < t; i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			return arg, i
		}
		if strings.IndexByte(arg, '=') > 0 {
			continue
		}
		if strings.HasPrefix(arg, "--") {
			if !runBoolFlags[arg] {
				i++
			}
		} else if vi := strings.IndexAny(arg, runValueShorthands); vi == len(arg)-1 {
			// Shorthand flag that requires an argument. Might be prefixed by shorthand booleans, e.g. -itl <label>.
			// The value is the next argument unless it's attached, as in -p8080:8080.
			i++
		}
	}
	return "", -1
}

// RunOptions returns the arguments that precede the image in the given docker run arguments.
// These are the options intended for docker run. All arguments are returned when no image is found.
func RunOptions(args []string) []string {
	if _, i := ImageArg(args); i >= 0 {
		return args[:i]
	}
	return args
}
