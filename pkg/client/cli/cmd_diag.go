package cli

import (
	"context"
	"fmt"
	"sync"

	"github.com/spf13/cobra"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
)

type diagInfo struct {
}

func diagCommand() *cobra.Command {
	di := diagInfo{}
	cmd := &cobra.Command{
		Use:   "test",
		Args:  cobra.NoArgs,
		Short: "Test telepresence operability",
		RunE:  di.runDiags,
	}
	return cmd
}

// runDiags runs tests in parallel, and prints the results of these
// tests as they finish. Tests can call other tests, forming test
// chains.
func (di *diagInfo) runDiags(cmd *cobra.Command, _ []string) error {
	err := withConnector(cmd, false, func(ctx context.Context, cc connector.ConnectorClient, ci *connector.ConnectInfo) error {
		err := cliutil.WithManager(ctx, func(ctx context.Context, mc manager.ManagerClient) error {

			testCapacity := 10 // capacity must be > number of parallel tests
			diagQueue := make([]Diag, 0, testCapacity)
			ch := make(chan DiagResult, testCapacity)
			baseDiag := BaseDiag{cmd: cmd, ch: ch, mc: mc, cc: cc}
			diagQueue = append(diagQueue, &Version{BaseDiag: &baseDiag})

			wg := sync.WaitGroup{}
			wg.Add(len(diagQueue))
			for _, d := range diagQueue {
				go func(diag Diag) {
					defer wg.Done()
					runDiag(diag)
				}(d)
			}

			go func() {
				wg.Wait()
				close(ch)
			}()

			for result := range ch {
				if result.Err() != nil {
					cmd.OutOrStdout().Write([]byte(fmt.Sprintf("Test %s FAILED during %s: %v\n", result.DiagName(), result.State(), result.Err())))
				} else {
					cmd.OutOrStdout().Write([]byte(fmt.Sprintf("Test %s PASSED\n", result.DiagName())))
				}
			}

			return nil // withman return
		})
		if err != nil { // TODO handle no manager
			return err
		}
		return nil // withconn return
	})
	if err != nil { // TODO handle no conn
		return err
	}

	return nil
}

func runDiag(diag Diag) {
	err := diag.setup()
	if err != nil {
		diag.resultChan() <- &BaseResult{
			state:    "setup",
			diagName: diag.diagName(),
			err:      err,
		}
	} else {
		defer func() {
			err := diag.teardown()
			if err != nil {
				diag.resultChan() <- &BaseResult{
					state:    "teardown",
					diagName: diag.diagName(),
					err:      err,
				}
			}
		}()

		err = diag.run()
		diag.resultChan() <- &BaseResult{
			state:    "runtime",
			diagName: diag.diagName(),
			err:      err,
		}
		if err != nil {
			diag.fail(err)
		} else {
			diag.pass()
		}
	}
}
