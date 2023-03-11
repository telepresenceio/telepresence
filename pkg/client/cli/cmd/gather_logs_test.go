package cmd

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"regexp"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/connect"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

func Test_gatherLogsZipFiles(t *testing.T) {
	type testcase struct {
		name string
		// We use these two slices so it's easier to write tests knowing which
		// files are expected to exist and which aren't. These slices are combined
		// prior to calling zipFiles in the tests.
		realFileNames []string
		fakeFileNames []string
		fileDir       string
	}
	testCases := []testcase{
		{
			name:          "successfulZipAllFiles",
			realFileNames: []string{"file1.log", "file2.log", "diff_name.log"},
			fakeFileNames: []string{},
			fileDir:       "testdata/zipDir",
		},
		{
			name:          "successfulZipSomeFiles",
			realFileNames: []string{"file1.log", "file2.log"},
			fakeFileNames: []string{},
			fileDir:       "testdata/zipDir",
		},
		{
			name:          "successfulZipNoFiles",
			realFileNames: []string{},
			fakeFileNames: []string{},
			fileDir:       "testdata/zipDir",
		},
		{
			name:          "zipOneIncorrectFile",
			realFileNames: []string{"file1.log", "file2.log", "diff_name.log"},
			fakeFileNames: []string{"notreal.log"},
			fileDir:       "testdata/zipDir",
		},
		{
			name:          "zipIncorrectDir",
			realFileNames: []string{},
			fakeFileNames: []string{"file1.log", "file2.log", "diff_name.log"},
			fileDir:       "testdata/fakeZipDir",
		},
	}

	for _, tc := range testCases {
		tcName := tc.name
		tc := tc
		t.Run(tcName, func(t *testing.T) {
			var fileNames []string
			fileNames = append(fileNames, tc.realFileNames...)
			fileNames = append(fileNames, tc.fakeFileNames...)
			if tc.fileDir != "" {
				for i := range fileNames {
					fileNames[i] = fmt.Sprintf("%s/%s", tc.fileDir, fileNames[i])
				}
			}
			outputDir := t.TempDir()
			err := zipFiles(fileNames, fmt.Sprintf("%s/logs.zip", outputDir))
			// If we put in fakeFileNames, then we verify we get the errors we expect
			if len(tc.fakeFileNames) > 0 {
				for _, name := range tc.fakeFileNames {
					assert.Contains(t, err.Error(), fmt.Sprintf("failed adding %s/%s to zip file", tc.fileDir, name))
				}
			} else {
				require.NoError(t, err)
			}

			// Ensure the files in the zip match the files that wer zipped
			zipReader, err := zip.OpenReader(fmt.Sprintf("%s/logs.zip", outputDir))
			require.NoError(t, err)
			defer zipReader.Close()

			for _, f := range zipReader.File {
				// Ensure the file was actually supposed to be in the zip
				assert.Contains(t, tc.realFileNames, f.Name)

				filesEqual, err := checkZipEqual(f, "testdata/zipDir")
				require.NoError(t, err)
				assert.True(t, filesEqual)
			}

			// Ensure that only the "real files" were added to the zip file
			assert.Equal(t, len(tc.realFileNames), len(zipReader.File))
		})
	}
}

func Test_gatherLogsCopyFiles(t *testing.T) {
	type testcase struct {
		name        string
		srcFileName string
		fileDir     string
		outputDir   string
		errExpected bool
	}
	testCases := []testcase{
		{
			name:        "successfulCopyFile",
			srcFileName: "file1.log",
			fileDir:     "testdata/zipDir",
			outputDir:   "",
			errExpected: false,
		},
		{
			name:        "failSrcFile",
			srcFileName: "fake_file.log",
			fileDir:     "testdata/zipDir",
			outputDir:   "",
			errExpected: true,
		},
		{
			name:        "failDstFile",
			srcFileName: "file1.log",
			fileDir:     "testdata/zipDir",
			outputDir:   "notarealdir",
			errExpected: true,
		},
	}
	for _, tc := range testCases {
		tcName := tc.name
		tc := tc
		t.Run(tcName, func(t *testing.T) {
			if tc.outputDir == "" {
				tc.outputDir = t.TempDir()
			}
			dstFile := fmt.Sprintf("%s/copiedFile.log", tc.outputDir)
			srcFile := fmt.Sprintf("%s/%s", tc.fileDir, tc.srcFileName)
			err := copyFiles(dstFile, srcFile)
			if tc.errExpected {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				require.NoError(t, err)
				// when there's no error message, we validate that the file was
				// copied correctly
				dstContent, err := os.ReadFile(dstFile)
				require.NoError(t, err)

				srcContent, err := os.ReadFile(srcFile)
				require.NoError(t, err)

				assert.Equal(t, string(dstContent), string(srcContent))
			}
		})
	}
}

func Test_gatherLogsNoK8s(t *testing.T) {
	type testcase struct {
		name       string
		outputFile string
		daemons    string
		errMsg     string
	}
	testCases := []testcase{
		{
			name:       "successfulZipAllDaemonLogs",
			outputFile: "",
			daemons:    "all",
			errMsg:     "",
		},
		{
			name:       "successfulZipOnlyRootLogs",
			outputFile: "",
			daemons:    "root",
			errMsg:     "",
		},
		{
			name:       "successfulZipOnlyConnectorLogs",
			outputFile: "",
			daemons:    "user",
			errMsg:     "",
		},
		{
			name:       "successfulZipNoDaemonLogs",
			outputFile: "",
			daemons:    "None",
			errMsg:     "",
		},
		{
			name:       "incorrectDaemonFlagValue",
			outputFile: "",
			daemons:    "notARealFlagValue",
			errMsg:     "Options for --daemons are: all, root, user, or None",
		},
	}

	for _, tc := range testCases {
		tcName := tc.name
		tc := tc
		t.Run(tcName, func(t *testing.T) {
			// Use this time to validate that the zip file says the
			// files inside were modified after the test started.
			startTime := time.Now()
			// Prepare the context + use our testdata log dir for these tests
			ctx := dlog.NewTestContext(t, false)
			testLogDir := "testdata/testLogDir"
			ctx = filelocation.WithAppUserLogDir(ctx, testLogDir)
			ctx = connect.WithCommandInitializer(ctx, connect.CommandInitializer)

			// this isn't actually used for our unit tests, but is needed for the function
			// when it is getting logs from k8s components
			cmd := &cobra.Command{}

			// override the outputFile
			outputDir := t.TempDir()
			if tc.outputFile == "" {
				tc.outputFile = fmt.Sprintf("%s/telepresence_logs.zip", outputDir)
			}
			stdout := dlog.StdLogger(ctx, dlog.LogLevelInfo).Writer()
			stderr := dlog.StdLogger(ctx, dlog.LogLevelError).Writer()
			cmd.SetOut(stdout)
			cmd.SetErr(stderr)
			cmd.SetContext(ctx)
			gl := &gatherLogsCommand{
				outputFile: tc.outputFile,
				daemons:    tc.daemons,
				// We will test other values of this in our integration tests since
				// they require a kubernetes cluster
				trafficAgents:  "None",
				trafficManager: false,
			}

			// Ensure we can create a zip of the logs
			err := gl.gatherLogs(cmd, nil)
			if tc.errMsg != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.errMsg)
			} else {
				require.NoError(t, err)

				// Validate that the zip file only contains the files we expect
				zipReader, err := zip.OpenReader(tc.outputFile)
				require.NoError(t, err)
				defer zipReader.Close()

				var regexStr string
				switch gl.daemons {
				case "all":
					regexStr = "cli|connector|daemon"
				case "root":
					regexStr = "daemon"
				case "user":
					regexStr = "connector"
				case "None":
					regexStr = "a^" // impossible to match
				default:
					// We shouldn't hit this
					t.Fatal("Used an option for daemon that is impossible")
				}
				for _, f := range zipReader.File {
					// Ensure the file was actually supposed to be in the zip
					assert.Regexp(t, regexp.MustCompile(regexStr), f.Name)

					filesEqual, err := checkZipEqual(f, testLogDir)
					require.NoError(t, err)
					assert.True(t, filesEqual)

					// Ensure the zip file metadata is correct (e.g. not the
					// default which is 1979) that it was modified after the
					// test started.
					// This test is incredibly fast (within a second) so we
					// convert the times to unix timestamps (to get us to
					// nearest seconds) and ensure the unix timestamp for the
					// zip file is not less than the unix timestamp for the
					// start time.
					// If this ends up being flakey, we can move the start
					// time out of the test loop and add a sleep for a second
					// to ensure nothing weird could happen with rounding.
					assert.False(t,
						f.FileInfo().ModTime().Unix() < startTime.Unix(),
						fmt.Sprintf("Start time: %d, file time: %d",
							startTime.Unix(),
							f.FileInfo().ModTime().Unix()))
				}
			}
		})
	}
}

func Test_gatherLogsGetPodName(t *testing.T) {
	podNames := []string{
		"echo-auto-inject-64323-3454.default",
		"echo-easy-141245-23432.ambassador",
		"traffic-manager-123214-2332.ambassador",
	}
	podMapping := []string{
		"pod-1.namespace-1",
		"pod-2.namespace-2",
		"traffic-manager.namespace-2",
	}

	// We need a fresh anonymizer for each test
	anonymizer := &anonymizer{
		namespaces: make(map[string]string),
		podNames:   make(map[string]string),
	}
	// Get the newPodName for each pod
	for _, podName := range podNames {
		newPodName := anonymizer.getPodName(podName)
		require.NotEqual(t, podName, newPodName)
	}
	// Ensure the anonymizer contains the total expected values
	require.Equal(t, 3, len(anonymizer.podNames))
	require.Equal(t, 2, len(anonymizer.namespaces))

	// Ensure the podNames were anonymized correctly
	for i := range podNames {
		require.Equal(t, podMapping[i], anonymizer.podNames[podNames[i]])
	}

	// Ensure the namespaces were anonymized correctly
	require.Equal(t, "namespace-1", anonymizer.namespaces["default"])
	require.Equal(t, "namespace-2", anonymizer.namespaces["ambassador"])
}

func Test_gatherLogsAnonymizeLogs(t *testing.T) {
	anonymizer := &anonymizer{
		namespaces: map[string]string{
			"default":    "namespace-1",
			"ambassador": "namespace-2",
		},
		// these names are specific because they come from the test data
		podNames: map[string]string{
			"echo-auto-inject-6496f77cbd-n86nc.default":   "pod-1.namespace-1",
			"traffic-manager-5c69859f94-g4ntj.ambassador": "traffic-manager.namespace-2",
		},
	}

	testLogDir := "testdata/testLogDir"
	outputDir := t.TempDir()
	files := []string{"echo-auto-inject-6496f77cbd-n86nc", "traffic-manager-5c69859f94-g4ntj"}
	for _, file := range files {
		// The anonymize function edits files in place
		// so copy the files before we do that
		srcFile := fmt.Sprintf("%s/%s", testLogDir, file)
		dstFile := fmt.Sprintf("%s/%s", outputDir, file)
		err := copyFiles(dstFile, srcFile)
		require.NoError(t, err)

		err = anonymizer.anonymizeLog(dstFile)
		require.NoError(t, err)

		// Now verify things have actually been anonymized
		anonFile, err := os.ReadFile(dstFile)
		require.NoError(t, err)
		require.NotContains(t, string(anonFile), "echo-auto-inject")
		require.NotContains(t, string(anonFile), "default")
		require.NotContains(t, string(anonFile), "ambassador")

		// Both logs make reference to "echo-auto-inject" so we
		// validate that "pod-1" appears in both logs
		require.Contains(t, string(anonFile), "pod-1")
	}
}

func Test_gatherLogsSignificantPodNames(t *testing.T) {
	type testcase struct {
		name    string
		podName string
		results []string
	}
	testCases := []testcase{
		{
			name:    "deploymentPod",
			podName: "echo-easy-867b648b88-zjsp2",
			results: []string{
				"echo-easy-867b648b88-zjsp2",
				"echo-easy-867b648b88",
				"echo-easy",
			},
		},
		{
			name:    "statefulSetPod",
			podName: "echo-easy-0",
			results: []string{
				"echo-easy-0",
				"echo-easy",
			},
		},
		{
			name:    "unknownName",
			podName: "notarealname",
			results: []string{},
		},
		{
			name:    "followPatternNotFullName",
			podName: "a123b",
			results: []string{},
		},
		{
			name:    "emptyName",
			podName: "",
			results: []string{},
		},
	}

	for _, tc := range testCases {
		tcName := tc.name
		tc := tc
		// We need a fresh anonymizer for each test
		t.Run(tcName, func(t *testing.T) {
			sigPodNames := getSignificantPodNames(tc.podName)
			require.Equal(t, tc.results, sigPodNames)
		})
	}
}

// ReadZip reads a zip file and returns the []byte string. Used in tests for
// checking that a zipped file's contents are correct. Exported since it is
// also used in telepresence_test.go.
func ReadZip(zippedFile *zip.File) ([]byte, error) {
	fileReader, err := zippedFile.Open()
	if err != nil {
		return nil, err
	}

	fileContent, err := io.ReadAll(fileReader)
	if err != nil {
		return nil, err
	}
	return fileContent, nil
}

// checkZipEqual is a helper function for validating that the zippedFile in the
// zip directory matches the file that was used to create the zip.
func checkZipEqual(zippedFile *zip.File, srcLogDir string) (bool, error) {
	dstContent, err := ReadZip(zippedFile)
	if err != nil {
		return false, err
	}
	srcContent, err := os.ReadFile(fmt.Sprintf("%s/%s", srcLogDir, zippedFile.Name))
	if err != nil {
		return false, err
	}

	return string(dstContent) == string(srcContent), nil
}
