// Command rtclean removes every regression-test resource left in the
// current kubeconfig context's cluster: everything labeled
// purpose=tp-rtest, the rtest-manager helm release, and running rtest
// daemons. Invoked via `make rtest-clean`.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

func main() {
	if err := rt.CleanAll(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "rtest-clean:", err)
		os.Exit(1)
	}
}
