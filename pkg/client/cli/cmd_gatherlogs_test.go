package cli

import (
	"archive/zip"
	"fmt"
	"io/ioutil"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
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

				zipFile, err := f.Open()
				if err != nil {
					t.Fatal(err)
				}
				// it's maybe not best to read them into memory, but since this is
				// just for tests and the files are very small, it's fine
				zipFileContents, err := ioutil.ReadAll(zipFile)
				if err != nil {
					t.Fatal(err)
				}
				testFileContents, err := os.ReadFile(fmt.Sprintf("testdata/zipDir/%s", f.Name))
				if err != nil {
					t.Fatal(err)
				}
				assert.Equal(t, string(zipFileContents), string(testFileContents))
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
		errMsg      string
	}
	testCases := []testcase{
		{
			name:        "successfulCopyFile",
			srcFileName: "file1.log",
			fileDir:     "testdata/zipDir",
			outputDir:   "",
			errMsg:      "",
		},
		{
			name:        "failSrcFile",
			srcFileName: "fake_file.log",
			fileDir:     "testdata/zipDir",
			outputDir:   "",
			errMsg:      "no such file or directory",
		},
		{
			name:        "failDstFile",
			srcFileName: "file1.log",
			fileDir:     "testdata/zipDir",
			outputDir:   "notarealdir",
			errMsg:      "no such file or directory",
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
			// if we have an errMsg, then we validate it is what we expect
			if tc.errMsg != "" {
				if err == nil {
					t.Fatal("expected there to be an error, got nil")
				}
				assert.Contains(t, err.Error(), tc.errMsg)
			} else {
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
		name string
		// We use these two slices so it's easier to write tests knowing which
		// files are exptected to exist and which aren't. These slices are combined
		// prior to calling zipFiles in the tests.
		realFileNames []string
		fakeFileNames []string
		fileDir       string
	}
	testCases := []testcase{}

	for _, tc := range testCases {
		tcName := tc.name
		//tc := tc
		t.Run(tcName, func(t *testing.T) {
		})
	}
}
