package client_test

import (
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/datawire/ambassador/pkg/metriton"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

func TestInstallID(t *testing.T) {
	errMsg := "is a directory"
	if runtime.GOOS == "windows" {
		errMsg = "The handle is invalid."
	}
	type testcase struct {
		InputGOOS    string
		InputEnv     map[string]string
		InputHomeDir map[string]string

		ExpectedID      string
		ExpectedErr     string
		ExpectedExtra   map[string]interface{}
		ExpectedHomeDir map[string]string
	}
	testcases := map[string]testcase{
		"linux-xdg": {
			InputGOOS: "linux",
			InputEnv: map[string]string{
				"XDG_CONFIG_HOME": "$HOME/other-config",
			},
			InputHomeDir: map[string]string{
				".config/telepresence/id":       "tp1-id",
				"other-config/edgectl/id":       "edgectl-id",
				"other-config/telepresence2/id": "tp2.1-id",
				"other-config/telepresence/id":  "tp2-id",
			},
			ExpectedID: "tp2-id",
			ExpectedExtra: map[string]interface{}{
				"install_id_telepresence-1":     "tp1-id",
				"install_id_edgectl":            "edgectl-id",
				"install_id_telepresence-2<2.1": "tp2.1-id",
				"install_id_telepresence-2":     nil,
				"new_install":                   false,
			},
		},
		"linux": {
			InputGOOS: "linux",
			InputHomeDir: map[string]string{
				".config/edgectl/id":       "edgectl-id",
				".config/telepresence2/id": "tp2.1-id",
				".config/telepresence/id":  "tp-id",
			},
			ExpectedID: "tp-id",
			ExpectedExtra: map[string]interface{}{
				"install_id_telepresence-1":     nil,
				"install_id_edgectl":            "edgectl-id",
				"install_id_telepresence-2<2.1": "tp2.1-id",
				"install_id_telepresence-2":     nil,
				"new_install":                   false,
			},
		},
		"darwin-xdg": {
			InputGOOS: "darwin",
			InputEnv: map[string]string{
				"XDG_CONFIG_HOME": "$HOME/other-config",
			},
			InputHomeDir: map[string]string{
				".config/telepresence/id":                     "tp1-id",
				"other-config/edgectl/id":                     "edgectl-id",
				"other-config/telepresence2/id":               "tp2.1-id",
				"Library/Application Support/telepresence/id": "tp2-id",
			},
			ExpectedID: "tp2-id",
			ExpectedExtra: map[string]interface{}{
				"install_id_telepresence-1":     "tp1-id",
				"install_id_edgectl":            "edgectl-id",
				"install_id_telepresence-2<2.1": "tp2.1-id",
				"install_id_telepresence-2":     nil,
				"new_install":                   false,
			},
		},
		"darwin": {
			InputGOOS: "darwin",
			InputHomeDir: map[string]string{
				".config/telepresence/id":                     "tp1-id",
				".config/edgectl/id":                          "edgectl-id",
				".config/telepresence2/id":                    "tp2.1-id",
				"Library/Application Support/telepresence/id": "tp2-id",
			},
			ExpectedID: "tp2-id",
			ExpectedExtra: map[string]interface{}{
				"install_id_telepresence-1":     "tp1-id",
				"install_id_edgectl":            "edgectl-id",
				"install_id_telepresence-2<2.1": "tp2.1-id",
				"install_id_telepresence-2":     nil,
				"new_install":                   false,
			},
		},
		"badfiles": {
			InputGOOS: "linux",
			InputEnv: map[string]string{
				"XDG_CONFIG_HOME": "$HOME/other-config",
			},
			InputHomeDir: map[string]string{
				".config/telepresence/id/x":       "tp1-id",
				"other-config/edgectl/id/x":       "edgectl-id",
				"other-config/telepresence2/id/x": "tp2.1-id",
				"other-config/telepresence/id/x":  "tp2-id",
			},
			ExpectedID:  "00000000-0000-0000-0000-000000000000",
			ExpectedErr: fmt.Sprintf("read %s: %s", filepath.Join("$HOME", "other-config", "edgectl", "id"), errMsg),
			ExpectedExtra: map[string]interface{}{
				"install_id_telepresence-1":     nil,
				"install_id_edgectl":            nil,
				"install_id_telepresence-2<2.1": nil,
				"install_id_telepresence-2":     nil,
				"new_install":                   true,
			},
		},
		"upgrade-tp1": {
			InputGOOS: "linux",
			InputEnv: map[string]string{
				"XDG_CONFIG_HOME": "$HOME/other-config",
			},
			InputHomeDir: map[string]string{
				".config/telepresence/id": "tp1-id",
			},
			ExpectedID: "tp1-id",
			ExpectedExtra: map[string]interface{}{
				"install_id_telepresence-1":     nil,
				"install_id_edgectl":            nil,
				"install_id_telepresence-2<2.1": nil,
				"install_id_telepresence-2":     nil,
				"new_install":                   false,
			},
			ExpectedHomeDir: map[string]string{
				"other-config/telepresence/id": "tp1-id",
			},
		},
		"upgrade-edgectl": {
			InputGOOS: "linux",
			InputHomeDir: map[string]string{
				".config/edgectl/id": "edge-id",
			},
			ExpectedID: "edge-id",
			ExpectedExtra: map[string]interface{}{
				"install_id_telepresence-1":     nil,
				"install_id_edgectl":            nil,
				"install_id_telepresence-2<2.1": nil,
				"install_id_telepresence-2":     nil,
				"new_install":                   false,
			},
			ExpectedHomeDir: map[string]string{
				".config/telepresence/id": "edge-id",
			},
		},
		"upgrade-tp1-and-edgectl": {
			InputGOOS: "linux",
			InputEnv: map[string]string{
				"XDG_CONFIG_HOME": "$HOME/other-config",
			},
			InputHomeDir: map[string]string{
				".config/telepresence/id": "tp1-id",
				"other-config/edgectl/id": "edge-id",
			},
			ExpectedID: "tp1-id",
			ExpectedExtra: map[string]interface{}{
				"install_id_telepresence-1":     nil,
				"install_id_edgectl":            "edge-id",
				"install_id_telepresence-2<2.1": nil,
				"install_id_telepresence-2":     nil,
				"new_install":                   false,
			},
			ExpectedHomeDir: map[string]string{
				"other-config/telepresence/id": "tp1-id",
			},
		},
		"upgrade-tp2.1": {
			InputGOOS: "darwin",
			InputHomeDir: map[string]string{
				".config/telepresence2/id": "tp2.1-id",
			},
			ExpectedID: "tp2.1-id",
			ExpectedExtra: map[string]interface{}{
				"install_id_telepresence-1":     nil,
				"install_id_edgectl":            nil,
				"install_id_telepresence-2<2.1": nil,
				"install_id_telepresence-2":     nil,
				"new_install":                   false,
			},
			ExpectedHomeDir: map[string]string{
				"Library/Application Support/telepresence/id": "tp2.1-id",
			},
		},
		"fresh-install": {
			InputGOOS: "darwin",
			ExpectedExtra: map[string]interface{}{
				"install_id_telepresence-1":     nil,
				"install_id_edgectl":            nil,
				"install_id_telepresence-2<2.1": nil,
				"install_id_telepresence-2":     nil,
				"new_install":                   true,
			},
			ExpectedHomeDir: map[string]string{
				"Library/Application Support/telepresence/id": "${id}",
			},
		},
	}
	origEnv := os.Environ()
	for tcName, tcData := range testcases {
		tcData := tcData
		t.Run(tcName, func(t *testing.T) {
			ctx := dlog.NewTestContext(t, true)
			homedir := t.TempDir()
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
			ctx = filelocation.WithGOOS(ctx, tcData.InputGOOS)
			os.Clearenv()
			os.Setenv("HOME", homedir)
			for k, v := range tcData.InputEnv {
				os.Setenv(k, os.ExpandEnv(v))
			}

			for filename, filebody := range tcData.InputHomeDir {
				if err := os.MkdirAll(filepath.Dir(filepath.Join(homedir, filename)), 0755); err != nil {
					t.Fatal(err)
				}
				if err := ioutil.WriteFile(filepath.Join(homedir, filename), []byte(filebody), 0644); err != nil {
					t.Fatal(err)
				}
			}

			// Then do...
			scout := client.NewScout(ctx, "go-test")
			scout.Reporter.Endpoint = metriton.BetaEndpoint
			actualID := scout.Reporter.InstallID()
			actualErr, _ := scout.Reporter.BaseMetadata["install_id_error"].(string)

			// And expect...
			if tcData.ExpectedID == "" {
				assert.NotEqual(t, "", actualID)
			} else {
				assert.Equal(t, tcData.ExpectedID, actualID)
			}
			assert.Equal(t, os.ExpandEnv(tcData.ExpectedErr), actualErr)
			for k, v := range tcData.ExpectedExtra {
				assert.Equal(t, v, scout.Reporter.BaseMetadata[k], k)
			}
			os.Setenv("id", actualID)
			for filename, expectedFilebody := range tcData.ExpectedHomeDir {
				fileBytes, err := ioutil.ReadFile(filepath.Join(homedir, filename))
				assert.NoError(t, err)
				assert.Equal(t, os.ExpandEnv(expectedFilebody), string(fileBytes), filename)
			}
		})
	}
}
