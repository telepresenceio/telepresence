package itest

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
)

type itestConfig struct {
	Env map[string]string `json:"env,omitempty"`
}

func LoadEnv(ctx context.Context) context.Context {
	cf := filepath.Join(filelocation.AppUserConfigDir(ctx), "itest.yml")
	data, err := os.ReadFile(cf)
	if err != nil {
		if !os.IsNotExist(err) {
			getT(ctx).Fatal(cf, err)
		}
		return ctx
	}

	var ic itestConfig
	if err := yaml.Unmarshal(data, &ic); err != nil {
		getT(ctx).Fatal(cf, err)
		return ctx
	}

	env := os.Environ()
	dosEnv := make(dos.MapEnv, len(env))
	for _, ep := range env {
		if ix := strings.IndexByte(ep, '='); ix > 0 {
			dosEnv[ep[:ix]] = ep[ix+1:]
		}
	}
	maps.Merge(dosEnv, ic.Env)
	return dos.WithEnv(ctx, dosEnv)
}
