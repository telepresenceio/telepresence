package flags

import (
	"fmt"

	"github.com/spf13/pflag"
)

func AddUnique(dst *pflag.FlagSet, src *pflag.FlagSet) (err error) {
	src.VisitAll(func(f *pflag.Flag) {
		if err != nil {
			return
		}
		if dst.Lookup(f.Name) != nil {
			err = fmt.Errorf("flag %q from flagset %q is already present in flagset %q", f.Name, src.Name(), dst.Name())
			return
		}
		if f.Shorthand != "" && dst.ShorthandLookup(f.Shorthand) != nil {
			f.Shorthand = ""
		}
		dst.AddFlag(f)
	})
	return err
}
