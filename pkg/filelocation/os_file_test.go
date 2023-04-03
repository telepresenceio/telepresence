package filelocation

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/datawire/dlib/dlog"
)

func TestUser(t *testing.T) {
	type testcase struct {
		InputGOOS string
		InputHOME string
		InputEnv  map[string]string

		ExpectedHomeDir   string
		ExpectedCacheDir  string
		ExpectedConfigDir string
	}
	testcases := map[string]testcase{
		"linux": {
			InputGOOS: "linux",
			InputEnv: map[string]string{
				"HOME": "/realhome",
			},
			ExpectedHomeDir:   "/realhome",
			ExpectedCacheDir:  "/realhome/.cache",
			ExpectedConfigDir: "/realhome/.config",
		},
		"linux-withhome": {
			InputGOOS: "linux",
			InputHOME: "/testhome",
			InputEnv: map[string]string{
				"HOME": "/realhome",
			},
			ExpectedHomeDir:   "/testhome",
			ExpectedCacheDir:  "/testhome/.cache",
			ExpectedConfigDir: "/testhome/.config",
		},
		"linux-xdg": {
			InputGOOS: "linux",
			InputEnv: map[string]string{
				"HOME":            "/realhome",
				"XDG_CACHE_HOME":  "/realhome/xdg-cache",
				"XDG_CONFIG_HOME": "/realhome/xdg-config",
			},
			ExpectedHomeDir:   "/realhome",
			ExpectedCacheDir:  "/realhome/xdg-cache",
			ExpectedConfigDir: "/realhome/xdg-config",
		},
		"linux-xdg-withhome": {
			InputGOOS: "linux",
			InputHOME: "/testhome",
			InputEnv: map[string]string{
				"HOME":            "/realhome",
				"XDG_CACHE_HOME":  "/realhome/xdg-config",
				"XDG_CONFIG_HOME": "/realhome/xdg-config",
			},
			ExpectedHomeDir:   "/testhome",
			ExpectedCacheDir:  "/testhome/.cache",
			ExpectedConfigDir: "/testhome/.config",
		},
		"darwin": {
			InputGOOS: "darwin",
			InputEnv: map[string]string{
				"HOME": "/realhome",
			},
			ExpectedHomeDir:   "/realhome",
			ExpectedCacheDir:  "/realhome/Library/Caches",
			ExpectedConfigDir: "/realhome/Library/Application Support",
		},
		"darwin-withhome": {
			InputGOOS: "darwin",
			InputHOME: "/testhome",
			InputEnv: map[string]string{
				"HOME": "/realhome",
			},
			ExpectedHomeDir:   "/testhome",
			ExpectedCacheDir:  "/testhome/Library/Caches",
			ExpectedConfigDir: "/testhome/Library/Application Support",
		},
	}
	origEnv := os.Environ()
	for tcName, tcData := range testcases {
		tcData := tcData
		t.Run(tcName, func(t *testing.T) {
			if runtime.GOOS == "windows" {
				splitAndRejoin := func(path *string) {
					if path != nil && *path != "" {
						ps := strings.Split(*path, "/")
						if ps[0] == "" {
							ps[0] = "\\"
						}
						*path = filepath.Join(ps...)
					}
				}
				splitAndRejoinEnv := func(env map[string]string) {
					for k, v := range env {
						splitAndRejoin(&v)
						env[k] = v
					}
				}
				splitAndRejoin(&tcData.InputHOME)
				splitAndRejoin(&tcData.ExpectedHomeDir)
				splitAndRejoin(&tcData.ExpectedCacheDir)
				splitAndRejoin(&tcData.ExpectedConfigDir)
				splitAndRejoinEnv(tcData.InputEnv)
			}
			ctx := dlog.NewTestContext(t, true)
			defer func() {
				os.Clearenv()
				for _, kv := range origEnv {
					parts := strings.SplitN(kv, "=", 2)
					if len(parts) != 2 {
						continue
					}
					os.Setenv(parts[0], parts[1])
				}
			}()

			// Given...
			ctx = WithGOOS(ctx, tcData.InputGOOS)
			if tcData.InputHOME != "" {
				ctx = WithUserHomeDir(ctx, tcData.InputHOME)
			}
			os.Clearenv()
			for k, v := range tcData.InputEnv {
				os.Setenv(k, v)
			}

			// Then do...
			actualHomePath := UserHomeDir(ctx)
			actualCachePath := userCacheDir(ctx)
			actualConfigPath := UserConfigDir(ctx)

			// And expect...

			assert.Equal(t, tcData.ExpectedHomeDir, actualHomePath)
			assert.Equal(t, tcData.ExpectedCacheDir, actualCachePath)
			assert.Equal(t, tcData.ExpectedConfigDir, actualConfigPath)
		})
	}
}
