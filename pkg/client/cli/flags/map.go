package flags

import (
	"github.com/spf13/pflag"

	"github.com/telepresenceio/telepresence/v2/pkg/slice"
)

// Map returns a map of the flags that has been modified in the given FlagSet.
func Map(flags *pflag.FlagSet) map[string]string {
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
