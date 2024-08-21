package itest

import (
	"context"
	"fmt"
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
	var icEnv map[string]string
	if err != nil {
		if !os.IsNotExist(err) {
			getT(ctx).Fatal(cf, err)
		}
	} else {
		var ic itestConfig
		if err := yaml.Unmarshal(data, &ic); err != nil {
			getT(ctx).Fatal(cf, err)
			return ctx
		}
		icEnv = ic.Env
	}

	if icEnv == nil {
		icEnv = make(map[string]string)
	}

	env := os.Environ()
	dosEnv := make(dos.MapEnv, len(env))
	for _, ep := range env {
		if ix := strings.IndexByte(ep, '='); ix > 0 {
			dosEnv[ep[:ix]] = ep[ix+1:]
		}
	}

	maps.Merge(dosEnv, icEnv)

	// Ensure that build-output/bin is on the path
	buildBin := filepath.Join(BuildOutput(ctx), "bin")
	path, ok := dosEnv["PATH"]
	if ok {
		dosEnv["PATH"] = fmt.Sprintf("%s%c%s", buildBin, os.PathListSeparator, path)
	} else {
		dosEnv["PATH"] = buildBin
	}
	return dos.WithEnv(ctx, dosEnv)
}

func BuildOutput(ctx context.Context) string {
	return filepath.Join(GetModuleRoot(ctx), "build-output")
}
