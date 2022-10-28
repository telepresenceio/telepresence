package cliconfig

import (
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sync"

	"github.com/fsnotify/fsnotify"

	"github.com/datawire/dlib/dlog"
)

const cfgFileName = "client.json"

type CLIConfigWatcher interface {
	Run(ctx context.Context) error
	GetConfigJson() (string, error)
}

type cliConfig struct {
	mountPath string
	mu        sync.RWMutex
	cfgJson   string
}

func NewCLIConfigWatcher(mountPath string) (CLIConfigWatcher, error) {
	watcher := &cliConfig{
		mountPath: mountPath,
	}
	return watcher, nil
}

func (c *cliConfig) Run(ctx context.Context) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()
	// For some reason if we watch the directory itself no events are received for the file.
	// If we watch the file instead, we'll see it be deleted and re-created when kubernetes
	// updates the symlink.
	path := path.Join(c.mountPath, cfgFileName)
	dlog.Debugf(ctx, "Setting up watcher for %s", path)
	if err = w.Add(path); err != nil {
		return fmt.Errorf("failed to watch %s: %v", path, err)
	}
	if err := c.refreshFile(ctx); err != nil {
		return fmt.Errorf("failed to read initial config: %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-w.Errors:
			dlog.Error(ctx, err)
		case event := <-w.Events:
			dlog.Debugf(ctx, "Received event on configmap mount: %s", event)
			if event.Op&(fsnotify.Remove) != 0 {
				if err := w.Add(path); err != nil {
					return fmt.Errorf("failed to watch %s: %v", path, err)
				}
				if err := c.refreshFile(ctx); err != nil {
					return err
				}
			} else if event.Op&(fsnotify.Write|fsnotify.Create) != 0 {
				if err := c.refreshFile(ctx); err != nil {
					return err
				}
			}
		}
	}
}

func (c *cliConfig) refreshFile(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	f, err := os.Open(filepath.Join(c.mountPath, cfgFileName))
	if err != nil {
		return fmt.Errorf("failed to open client config file: %w", err)
	}
	defer f.Close()
	b, err := io.ReadAll(f)
	if err != nil {
		return fmt.Errorf("failed to read client config file: %w", err)
	}
	c.cfgJson = string(b)
	dlog.Debugf(ctx, "Refreshed client config: %s", c.cfgJson)
	return nil
}

func (c *cliConfig) GetConfigJson() (string, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.cfgJson, nil
}
