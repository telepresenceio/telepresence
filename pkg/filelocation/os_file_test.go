package filelocation

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/datawire/dlib/dlog"
)

func TestUser(t *testing.T) {
	type pathResult struct {
		Path string
		Err  error
	}
	type testcase struct {
		InputGOOS string
		InputHOME string
		InputEnv  map[string]string

		ExpectedHomeDir   pathResult
		ExpectedCacheDir  pathResult
		ExpectedConfigDir pathResult
	}
	testcases := map[string]testcase{
		"linux": {
			InputGOOS: "linux",
			InputEnv: map[string]string{
				"HOME": "/realhome",
			},
			ExpectedHomeDir:   pathResult{"/realhome", nil},
			ExpectedCacheDir:  pathResult{"/realhome/.cache", nil},
			ExpectedConfigDir: pathResult{"/realhome/.config", nil},
		},
		"linux-withhome": {
			InputGOOS: "linux",
			InputHOME: "/testhome",
			InputEnv: map[string]string{
				"HOME": "/realhome",
			},
			ExpectedHomeDir:   pathResult{"/testhome", nil},
			ExpectedCacheDir:  pathResult{"/testhome/.cache", nil},
			ExpectedConfigDir: pathResult{"/testhome/.config", nil},
		},
		"linux-xdg": {
			InputGOOS: "linux",
			InputEnv: map[string]string{
				"HOME":            "/realhome",
				"XDG_CACHE_HOME":  "/realhome/xdg-cache",
				"XDG_CONFIG_HOME": "/realhome/xdg-config",
			},
			ExpectedHomeDir:   pathResult{"/realhome", nil},
			ExpectedCacheDir:  pathResult{"/realhome/xdg-cache", nil},
			ExpectedConfigDir: pathResult{"/realhome/xdg-config", nil},
		},
		"linux-xdg-withhome": {
			InputGOOS: "linux",
			InputHOME: "/testhome",
			InputEnv: map[string]string{
				"HOME":            "/realhome",
				"XDG_CACHE_HOME":  "/realhome/xdg-config",
				"XDG_CONFIG_HOME": "/realhome/xdg-config",
			},
			ExpectedHomeDir:   pathResult{"/testhome", nil},
			ExpectedCacheDir:  pathResult{"/testhome/.cache", nil},
			ExpectedConfigDir: pathResult{"/testhome/.config", nil},
		},
		"darwin": {
			InputGOOS: "darwin",
			InputEnv: map[string]string{
				"HOME": "/realhome",
			},
			ExpectedHomeDir:   pathResult{"/realhome", nil},
			ExpectedCacheDir:  pathResult{"/realhome/Library/Caches", nil},
			ExpectedConfigDir: pathResult{"/realhome/Library/Application Support", nil},
		},
		"darwin-withhome": {
			InputGOOS: "darwin",
			InputHOME: "/testhome",
			InputEnv: map[string]string{
				"HOME": "/realhome",
			},
			ExpectedHomeDir:   pathResult{"/testhome", nil},
			ExpectedCacheDir:  pathResult{"/testhome/Library/Caches", nil},
			ExpectedConfigDir: pathResult{"/testhome/Library/Application Support", nil},
		},
	}
	origEnv := os.Environ()
	for tcName, tcData := range testcases {
		tcData := tcData
		t.Run(tcName, func(t *testing.T) {
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
			actualHomePath, actualHomeErr := UserHomeDir(ctx)
			actualCachePath, actualCacheErr := userCacheDir(ctx)
			actualConfigPath, actualConfigErr := UserConfigDir(ctx)

			// And expect...

			assert.Equal(t, tcData.ExpectedHomeDir.Path, actualHomePath)
			if tcData.ExpectedHomeDir.Err == nil {
				assert.NoError(t, actualHomeErr)
			} else {
				assert.Equal(t, tcData.ExpectedHomeDir.Err.Error(), actualHomeErr.Error())
			}

			assert.Equal(t, tcData.ExpectedCacheDir.Path, actualCachePath)
			if tcData.ExpectedCacheDir.Err == nil {
				assert.NoError(t, actualCacheErr)
			} else {
				assert.Equal(t, tcData.ExpectedCacheDir.Err.Error(), actualCacheErr.Error())
			}

			assert.Equal(t, tcData.ExpectedConfigDir.Path, actualConfigPath)
			if tcData.ExpectedConfigDir.Err == nil {
				assert.NoError(t, actualConfigErr)
			} else {
				assert.Equal(t, tcData.ExpectedConfigDir.Err.Error(), actualConfigErr.Error())
			}
		})
	}
}
