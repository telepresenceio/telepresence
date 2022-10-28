package cli

import (
	"github.com/spf13/pflag"
)

func kubeFlagMap(kubeFlags *pflag.FlagSet) map[string]string {
	kubeFlagMap := make(map[string]string, kubeFlags.NFlag())
	kubeFlags.VisitAll(func(flag *pflag.Flag) {
		if flag.Changed {
			kubeFlagMap[flag.Name] = flag.Value.String()
		}
	})
	return kubeFlagMap
}
