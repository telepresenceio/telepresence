package scout

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/metriton-go-client/metriton"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

func TestInstallID(t *testing.T) {
	type testcase struct {
		InputGOOS    string
		InputEnv     map[string]string
		InputHomeDir map[string]string

		ExpectedID      string
		ExpectedErr     string
		ExpectedExtra   map[string]any
		ExpectedHomeDir map[string]string
	}
	var testcases map[string]testcase
	if runtime.GOOS == "windows" {
		testcases = map[string]testcase{
			"fresh-install": {
				InputGOOS: "windows",
				ExpectedExtra: map[string]any{
					"install_id_telepresence-1":     nil,
					"install_id_edgectl":            nil,
					"install_id_telepresence-2<2.1": nil,
					"install_id_telepresence-2":     nil,
					"new_install":                   true,
				},
				ExpectedHomeDir: map[string]string{
					`AppData\Roaming\telepresence\id`: "${id}",
				},
			},
			"upgrade-tp2.1": {
				InputGOOS: "windows",
				InputHomeDir: map[string]string{
					`AppData\Roaming\telepresence\id`: "tp2.1-id",
				},
				ExpectedID: "tp2.1-id",
				ExpectedExtra: map[string]any{
					"install_id_telepresence-1":     nil,
					"install_id_edgectl":            nil,
					"install_id_telepresence-2<2.1": nil,
					"install_id_telepresence-2":     nil,
					"new_install":                   false,
				},
				ExpectedHomeDir: map[string]string{
					`AppData\Roaming\telepresence\id`: "tp2.1-id",
				},
			},
		}
	} else {
		errMsg := "is a directory"
		testcases = map[string]testcase{
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
				ExpectedExtra: map[string]any{
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
				ExpectedExtra: map[string]any{
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
				ExpectedExtra: map[string]any{
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
				ExpectedExtra: map[string]any{
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
				ExpectedErr: fmt.Sprintf("read %s: %s", filepath.Join("$HOME", "other-config", "telepresence", "id"), errMsg),
				ExpectedExtra: map[string]any{
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
				ExpectedExtra: map[string]any{
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
				ExpectedExtra: map[string]any{
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
				ExpectedExtra: map[string]any{
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
				ExpectedExtra: map[string]any{
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
				ExpectedExtra: map[string]any{
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
	}

	origEnv := os.Environ()

	ov := version.Version
	version.Version = "v0.0.0"
	defer func() { version.Version = ov }()

	for tcName, tcData := range testcases {
		tcData := tcData
		t.Run(tcName, func(t *testing.T) {
			if tcData.InputGOOS != runtime.GOOS {
				t.Skip()
				return
			}

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
			if tcData.InputGOOS == "windows" {
				os.Setenv("USERPROFILE", homedir)
			} else {
				os.Setenv("HOME", homedir)
			}
			for k, v := range tcData.InputEnv {
				os.Setenv(k, os.ExpandEnv(v))
			}

			for filename, filebody := range tcData.InputHomeDir {
				if err := os.MkdirAll(filepath.Dir(filepath.Join(homedir, filename)), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(homedir, filename), []byte(filebody), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			// Then do...
			scout := NewReporterForInstallType(ctx, "go-test", CLI, DefaultReportAnnotators, DefaultReportMutators).(*reporter)
			scout.reporter.Endpoint = metriton.BetaEndpoint
			actualID := scout.reporter.InstallID()
			actualErr, _ := scout.reporter.BaseMetadata["install_id_error"].(string)

			// And expect...
			if tcData.ExpectedID == "" {
				assert.NotEqual(t, "", actualID)
			} else {
				assert.Equal(t, tcData.ExpectedID, actualID)
			}
			assert.Equal(t, os.ExpandEnv(tcData.ExpectedErr), actualErr)
			for k, v := range tcData.ExpectedExtra {
				assert.Equal(t, v, scout.reporter.BaseMetadata[k], k)
			}
			os.Setenv("id", actualID)
			for filename, expectedFilebody := range tcData.ExpectedHomeDir {
				fileBytes, err := os.ReadFile(filepath.Join(homedir, filename))
				assert.NoError(t, err)
				assert.Equal(t, os.ExpandEnv(expectedFilebody), string(fileBytes), filename)
			}
		})
	}
}

func TestReport(t *testing.T) {
	const (
		mockInstallID = "00000000-1111-2222-3333-444444444444"
		mockMode      = "test-mode"
		mockAction    = "test-action"
	)
	type testcase struct {
		InputEnv         map[string]string
		Input            []Entry
		ExpectedMetadata map[string]any
		ReportAnnotators []ReportAnnotator
	}
	testcases := map[string]testcase{
		"without-additional-meta": {
			ExpectedMetadata: map[string]any{
				"action": mockAction,
				"mode":   mockMode,
				"goos":   runtime.GOOS,
				"goarch": runtime.GOARCH,
			},
		},
		"with-additional-scout-meta": {
			Input: []Entry{
				{
					Key:   "extra_field_1",
					Value: "extra value 1",
				},
				{
					Key:   "extra_field_2",
					Value: "extra value 2",
				},
			},
			ExpectedMetadata: map[string]any{
				"action":        mockAction,
				"mode":          mockMode,
				"goos":          runtime.GOOS,
				"goarch":        runtime.GOARCH,
				"extra_field_1": "extra value 1",
				"extra_field_2": "extra value 2",
			},
		},
		"with-additional-env-meta": {
			InputEnv: map[string]string{
				"TELEPRESENCE_REPORT_EXTRA_FIELD_1": "extra value 1",
				"TELEPRESENCE_REPORT_EXTRA_FIELD_2": "extra value 2",
			},
			ExpectedMetadata: map[string]any{
				"action":        mockAction,
				"mode":          mockMode,
				"goos":          runtime.GOOS,
				"goarch":        runtime.GOARCH,
				"extra_field_1": "extra value 1",
				"extra_field_2": "extra value 2",
			},
		},
		"with-additional-env-meta-overridden-by-default-and-scout-meta": {
			InputEnv: map[string]string{
				"TELEPRESENCE_REPORT_ACTION":        "should be overridden",
				"TELEPRESENCE_REPORT_EXTRA_FIELD_1": "should also be overridden",
			},
			Input: []Entry{
				{
					Key:   "extra_field_1",
					Value: "extra value 1",
				},
			},
			ExpectedMetadata: map[string]any{
				"action":        mockAction,
				"mode":          mockMode,
				"goos":          runtime.GOOS,
				"goarch":        runtime.GOARCH,
				"extra_field_1": "extra value 1",
			},
		},
		"with-scout-meta-overriding-default-meta": {
			Input: []Entry{
				{
					Key:   "mode",
					Value: "overridden mode",
				},
			},
			ExpectedMetadata: map[string]any{
				"action": mockAction,
				"mode":   "overridden mode",
				"goos":   runtime.GOOS,
				"goarch": runtime.GOARCH,
			},
		},
		"with-report-annotators": {
			Input: []Entry{
				{
					Key:   "mode",
					Value: "overridden mode",
				},
				{
					Key:   "extra_field",
					Value: "extra value",
				},
			},
			ReportAnnotators: []ReportAnnotator{
				func(data map[string]any) {
					data["action"] = "overridden action"
					data["annotation"] = "annotated value"
					data["extra_field"] = "annotated extra value"
				},
			},
			ExpectedMetadata: map[string]any{
				"action":      "overridden action",
				"mode":        "overridden mode",
				"goos":        runtime.GOOS,
				"goarch":      runtime.GOARCH,
				"extra_field": "extra value", // Not overridden by annotation
				"annotation":  "annotated value",
			},
		},
	}
	for tcName, tcData := range testcases {
		tcData := tcData
		t.Run(tcName, func(t *testing.T) {
			ctx := dlog.NewTestContext(t, true)
			origEnv := os.Environ()
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

			// Mock server capturing reports
			var capturedRequestBodies []map[string]any
			testServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				var body map[string]any
				bodyBytes, err := io.ReadAll(request.Body)
				if err != nil {
					t.Fatalf("Could not read request body: %v", err)
				}
				err = json.Unmarshal(bodyBytes, &body)
				if err != nil {
					t.Fatalf("Could not unmarshal request body: %v", err)
				}
				capturedRequestBodies = append(capturedRequestBodies, body)
			}))

			os.Unsetenv("SCOUT_DISABLE")
			// Given...
			for k, v := range tcData.InputEnv {
				os.Setenv(k, v)
			}

			setInstallIDFromFilesystem = func(ctx context.Context, installType InstallType, md map[string]any) (string, error) {
				return mockInstallID, nil
			}
			scout := NewReporterForInstallType(ctx, mockMode, CLI, tcData.ReportAnnotators, nil).(*reporter)
			scout.reporter.Endpoint = testServer.URL

			// Start scout report processing...
			sc, cancel := context.WithCancel(dcontext.WithSoftness(ctx))
			wg := &sync.WaitGroup{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				assert.NoError(t, scout.Run(sc))
			}()

			// Then do...
			scout.Report(ctx, mockAction, tcData.Input...)
			cancel()
			wg.Wait()

			// And expect...
			require.Len(t, capturedRequestBodies, 1)
			metadata := capturedRequestBodies[0]["metadata"].(map[string]any)

			expMeta := make(map[string]any)
			expMeta["index"] = 1.0
			for k, v := range scout.reporter.BaseMetadata {
				expMeta[k] = v
			}
			for k, v := range tcData.ExpectedMetadata {
				expMeta[k] = v
			}
			for expectedKey, expectedValue := range expMeta {
				assert.Equal(t, expectedValue, metadata[expectedKey])
			}
			for actualKey, actualValue := range metadata {
				_, ok := expMeta[actualKey]
				assert.True(t, ok, "%s = %v is unexpected", actualKey, actualValue)
			}
		})
	}
}
