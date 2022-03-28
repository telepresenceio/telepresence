package client_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/telepresenceio/telepresence/v2/pkg/client"
)

func TestGetInstallMechanism(t *testing.T) {
	type testcase struct {
		binaryPath               string
		symLinkPath              string
		expectedInstallMechanism string
		errFile                  bool
	}
	fakeExecDir := t.TempDir()
	testcases := map[string]testcase{
		"website-install": {
			binaryPath:               "telepresence",
			symLinkPath:              "",
			expectedInstallMechanism: "website",
			errFile:                  false,
		},
		"docker-install": {
			binaryPath:               "docker/telepresence",
			symLinkPath:              "",
			expectedInstallMechanism: "docker",
			errFile:                  false,
		},
		"docker-install-sym": {
			binaryPath:               "docker/telepresence",
			symLinkPath:              "telepresence",
			expectedInstallMechanism: "docker",
			errFile:                  false,
		},
		// we care about the underlying executable so even if someone
		// symlinks to make it *seem* like it's installed via docker
		// it will use the actual executable.
		"pseudo-symlink": {
			binaryPath:               "telepresence",
			symLinkPath:              "docker/telepresence",
			expectedInstallMechanism: "website",
			errFile:                  false,
		},
		"fail-executable-files": {
			binaryPath:               "telepresence",
			symLinkPath:              "",
			expectedInstallMechanism: "undetermined",
			errFile:                  true,
		},
	}
	if runtime.GOOS == "darwin" {
		testcases["brew-install"] = testcase{
			binaryPath:               "Cellar/telepresence",
			symLinkPath:              "",
			expectedInstallMechanism: "brew",
			errFile:                  false,
		}
		testcases["brew-install-sym"] = testcase{
			binaryPath:               "Cellar/telepresence",
			symLinkPath:              "telepresence",
			expectedInstallMechanism: "brew",
			errFile:                  false,
		}
	} else {
		// we *only* support brew for macOS so we should report website in
		// this case.
		testcases["brew-install"] = testcase{
			binaryPath:               "Cellar/telepresence",
			symLinkPath:              "",
			expectedInstallMechanism: "website",
			errFile:                  false,
		}
		testcases["brew-install-sym"] = testcase{
			binaryPath:               "Cellar/telepresence",
			symLinkPath:              "telepresence",
			expectedInstallMechanism: "website",
			errFile:                  false,
		}
	}

	createFile := func(fullFilePath string) error {
		err := os.MkdirAll(filepath.Dir(fullFilePath), os.ModePerm)
		if err != nil {
			return err
		}
		f, err := os.Create(fullFilePath)
		if err != nil {
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		return nil
	}
	for tcName, tcData := range testcases {
		tcData := tcData
		t.Run(tcName, func(t *testing.T) {
			// Create the fake binary for our test
			// We include the tcName in the filePath, so we don't have to worry about
			// named collisions or cleaning up after each test
			filePath := fmt.Sprintf("%s/%s/%s", fakeExecDir, tcName, tcData.binaryPath)
			assert.NoError(t, createFile(filePath))

			// Create symlink if specified
			if tcData.symLinkPath != "" && tcData.errFile {
				t.Fatalf("symLinkPath and errFile are mutually exclusive")
			}
			if tcData.symLinkPath != "" {
				symLinkFile := fmt.Sprintf("%s/%s/%s", fakeExecDir, tcName, tcData.symLinkPath)
				assert.NoError(t, os.MkdirAll(filepath.Dir(symLinkFile), os.ModePerm))
				assert.NoError(t, os.Symlink(filePath, symLinkFile))
				filePath = symLinkFile
			}

			if tcData.errFile {
				filePath = "/not/a/real/file"
			}
			// Validate the install mechanism is what we expect
			installMech, err := client.GetMechanismFromPath(filePath)
			assert.Equal(t, tcData.expectedInstallMechanism, installMech)
			if tcData.errFile {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
