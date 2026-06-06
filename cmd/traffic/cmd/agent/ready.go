package agent

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"
)

const (
	readyCheckTimeout = 5 * time.Second
	readyDir          = "/tmp/agent"
	readyFile         = readyDir + "/ready"
)

func ReadyMain(ctx context.Context, _ ...string) error {
	ctx, cancel := context.WithTimeout(ctx, readyCheckTimeout)
	defer cancel()

	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, err := os.Stat(readyFile)
		if err == nil {
			return nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("ready file %q did not appear within %s", readyFile, readyCheckTimeout)
		case <-ticker.C:
		}
	}
}
