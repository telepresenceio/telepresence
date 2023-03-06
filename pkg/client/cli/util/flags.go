package util

import (
	"fmt"
	"strings"

	"github.com/spf13/pflag"

	"github.com/telepresenceio/telepresence/v2/pkg/slice"
)

// FlagMap returns a map of the flags that has been modified in the given FlagSet.
func FlagMap(flags *pflag.FlagSet) map[string]string {
	if flags == nil {
		return nil
	}
	flagMap := make(map[string]string, flags.NFlag())
	flags.VisitAll(func(flag *pflag.Flag) {
		if flag.Changed {
			var v string
			if sv, ok := flag.Value.(pflag.SliceValue); ok {
				v = slice.AsCSV(sv.GetSlice())
			} else {
				v = flag.Value.String()
			}
			flagMap[flag.Name] = v
		}
	})
	return flagMap
}

// GetUnparsedFlagValue returns the value of a flag that has been provided after a "--" on the command
// line, and hence hasn't been parsed as a normal flag. Typical use case is:
//
//	telepresence intercept --docker-run ... -- --name <name>
func GetUnparsedFlagValue(args []string, flag string) (string, error) {
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
