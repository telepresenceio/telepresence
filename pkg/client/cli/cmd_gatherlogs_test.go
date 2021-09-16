package cli

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"regexp"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

func Test_gatherLogsZipFiles(t *testing.T) {
	type testcase struct {
		name string
		// We use these two slices so it's easier to write tests knowing which
		// files are exptected to exist and which aren't. These slices are combined
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
			err := zipFiles(fileNames, fmt.Sprintf("%s/blah.zip", outputDir))
			if err != nil {
				if len(tc.fakeFileNames) > 0 {
					for _, name := range tc.fakeFileNames {
						assert.Contains(t, err.Error(), fmt.Sprintf("failed adding %s/%s to zip file", tc.fileDir, name))
					}
				} else {
					t.Fatal(err)
				}
			}

			// Ensure the files in the zip match the files that wer zipped
			zipReader, err := zip.OpenReader(fmt.Sprintf("%s/blah.zip", outputDir))
			if err != nil {
				t.Fatal(err)
			}
			defer zipReader.Close()

			for _, f := range zipReader.File {
				// Ensure the file was actually supposed to be in the zip
				assert.Contains(t, tc.realFileNames, f.Name)

				filesEqual, err := checkZipEqual(f, "testdata/zipDir")
				if err != nil {
					t.Fatal(err)
				}
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
				// when there's no error message, we validate that the file was
				// copied correctly
				if err != nil {
					t.Fatal(err)
				}

				dstContent, err := os.ReadFile(dstFile)
				if err != nil {
					t.Fatal(err)
				}
				srcContent, err := os.ReadFile(srcFile)
				if err != nil {
					t.Fatal(err)
				}
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
			daemons:    "False",
			errMsg:     "",
		},
		{
			name:       "incorrectDaemonFlagValue",
			outputFile: "",
			daemons:    "notarealflag",
			errMsg:     "Options for --daemons are: all, root, user, or False",
		},
	}

	for _, tc := range testCases {
		tcName := tc.name
		tc := tc
		t.Run(tcName, func(t *testing.T) {
			// Prepare the context + use our testdata log dir for these tests
			ctx := dlog.NewTestContext(t, false)
			testLogDir := "testdata/testLogDir"
			ctx = filelocation.WithAppUserLogDir(ctx, testLogDir)

			// this isn't actually used for our unit tests, but is needed for the function
			// when it is getting logs from k8s components
			cmd := &cobra.Command{}

			// override the outputFile
			outputDir := t.TempDir()
			if tc.outputFile == "" {
				tc.outputFile = fmt.Sprintf("%s/telepresence_logs.zip", outputDir)
			}
			stdout := dlog.StdLogger(ctx, dlog.LogLevelInfo).Writer()
			gl := &gatherLogsArgs{
				outputFile: tc.outputFile,
				daemons:    tc.daemons,
				// We will test other values of this in our integration tests since
				// they require a kubernetes cluster
				trafficAgents:  "False",
				trafficManager: false,
			}

			// Ensure we can create a zip of the logs
			err := gl.gatherLogs(cmd, ctx, stdout)
			if tc.errMsg != "" {
				if err == nil {
					t.Fatal("error was expected")
				}
				assert.Contains(t, err.Error(), tc.errMsg)
			} else {
				if err != nil {
					t.Fatal(err)
				}

				// Validate that the zip file only contains the files we expect
				zipReader, err := zip.OpenReader(tc.outputFile)
				if err != nil {
					t.Fatal(err)
				}
				defer zipReader.Close()

				var regexStr string
				switch gl.daemons {
				case "all":
					regexStr = "connector|daemon"
				case "root":
					regexStr = "daemon"
				case "user":
					regexStr = "connector"
				case "False":
					regexStr = "!connector|!daemon"
				default:
					// We shouldn't hit this
					t.Fatal("Used an option for daemon that is impossible")
				}
				for _, f := range zipReader.File {
					// Ensure the file was actually supposed to be in the zip
					assert.Regexp(t, regexp.MustCompile(regexStr), f.Name)

					filesEqual, err := checkZipEqual(f, testLogDir)
					if err != nil {
						t.Fatal(err)
					}
					assert.True(t, filesEqual)
				}
			}
		})
	}
}

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
