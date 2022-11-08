package cliconfig_test

import (
	"context"
	"os"
	"path"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/internal/cliconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/log"
)

func TestCliConfigWatcher(t *testing.T) {
	ctx := dlog.WithLogger(context.Background(), log.NewTestLogger(t, dlog.LogLevelDebug))

	tmpdir := t.TempDir()
	expected := `
velvet: underground
one:
  two: false`

	writeFile := func(expected string) error {
		f, err := os.Create(path.Join(tmpdir, "client.yaml"))
		if err != nil {
			return err
		}
		if _, err = f.Write([]byte(expected)); err != nil {
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		return nil
	}
	if err := writeFile(expected); err != nil {
		t.Fatal(err)
	}
	watcher, err := cliconfig.NewWatcher(tmpdir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errCh := make(chan error)
	go func() {
		if err := watcher.Run(ctx); err != nil {
			errCh <- err
		}
		close(errCh)
	}()
	require.Eventually(t, func() bool {
		result := string(watcher.GetConfigYaml())
		if result != expected {
			t.Errorf("Expected %s got %s", expected, result)
			return false
		}
		return true
	}, 1*time.Second, 50*time.Millisecond)

	expected = `
the:
  - smiths
  - doors
  - stones
minute: 22`
	if err := writeFile(expected); err != nil {
		t.Fatal(err)
	}

	require.Eventually(t, func() bool {
		result := string(watcher.GetConfigYaml())
		if result != expected {
			t.Errorf("Expected %s got %s", expected, result)
			return false
		}
		return true
	}, 1*time.Second, 50*time.Millisecond)

	cancel()

	if err = <-errCh; err != nil {
		t.Fatal(err)
	}
}
