package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

func TestSweepMountPoints(t *testing.T) {
	root := t.TempDir()

	mkdir := func(name string) string {
		dir := filepath.Join(root, name)
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		return dir
	}
	touchOld := func(dir string) {
		old := time.Now().Add(-2 * time.Minute)
		if err := os.Chtimes(dir, old, old); err != nil {
			t.Fatal(err)
		}
	}

	fresh := mkdir("telfs-fresh") // empty, but just created

	old := mkdir("telfs-old") // empty and old: should be swept
	touchOld(old)

	oldNonEmpty := mkdir("telfs-old-busy") // old, but not empty: a live mount
	if err := os.WriteFile(filepath.Join(oldNonEmpty, "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	touchOld(oldNonEmpty)

	notTelfs := mkdir("other-dir") // old and empty, but not a telfs- directory
	touchOld(notTelfs)

	cfg := client.GetDefaultConfig()
	cfg.Intercept().MountsRoot = root
	ctx := client.WithConfig(context.Background(), cfg)

	sweepMountPoints(ctx, cfg)

	exists := func(dir string) bool {
		_, err := os.Stat(dir)
		return err == nil
	}
	if !exists(fresh) {
		t.Error("a freshly created, empty telfs- directory must survive the sweep")
	}
	if exists(old) {
		t.Error("an old, empty telfs- directory must be removed by the sweep")
	}
	if !exists(oldNonEmpty) {
		t.Error("an old, non-empty telfs- directory must survive the sweep")
	}
	if !exists(notTelfs) {
		t.Error("an old, empty, non-telfs- directory must survive the sweep")
	}
}
