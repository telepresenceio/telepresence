package itest

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/sirupsen/logrus"
	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/dos"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/json"
	"github.com/telepresenceio/telepresence/v2/pkg/maps"
)

type itestConfig struct {
	Env    map[string]string `json:"Env,omitempty"`
	Config client.Config     `json:"Config,omitempty"`
}

func LoadEnvAndConfig(ctx context.Context) context.Context {
	cf := filepath.Join(filelocation.AppUserConfigDir(ctx), "itest.yml")
	data, err := os.ReadFile(cf)
	var icEnv map[string]string
	icConfig := client.GetDefaultConfig()
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			getT(ctx).Fatal(cf, err)
		}
	} else {
		var ic itestConfig
		data, err := yaml.YAMLToJSON(data)
		if err == nil {
			ic.Config = icConfig
			ic.Config.LogLevels().UserDaemon = logrus.DebugLevel
			ic.Config.LogLevels().RootDaemon = logrus.DebugLevel
			err = json.Unmarshal(data, &ic, true)
		}
		if err != nil {
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

	ctx = client.WithConfig(ctx, icConfig)
	return dos.WithEnv(ctx, dosEnv)
}

func BuildOutput(ctx context.Context) string {
	return filepath.Join(GetModuleRoot(ctx), "build-output")
}
