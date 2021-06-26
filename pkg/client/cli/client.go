package cli

import (
	"context"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/cliutil"
)

// quit sends the quit message to the daemon and waits for it to exit.
func quit(ctx context.Context) error {
	// When the daemon shuts down, it will tell the connector to shut down.
	if err := cliutil.QuitDaemon(ctx); err != nil {
		return err
	}

	// But also do that ourselves; to ensure the connector is killed even if daemon isn't
	// running.  If the daemon already shut down the connector, then this is a no-op.
	if err := cliutil.QuitConnector(ctx); err != nil {
		return err
	}

	return nil
}
