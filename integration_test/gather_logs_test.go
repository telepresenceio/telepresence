package integration_test

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

func (s *multipleInterceptsSuite) TestGatherLogs_AllLogs() {
	require := s.Require()
	outputDir := s.T().TempDir()
	ctx := s.Context()
	outputFile := filepath.Join(outputDir, "allLogs.zip")
	s.cleanLogDir(ctx)
	itest.TelepresenceOk(ctx, "gather-logs", "--get-pod-yaml", "--output-file", outputFile)
	foundManager, foundAgents, yamlCount, fileNames := s.getZipData(outputFile)
	require.True(foundManager)
	require.Equal(s.ServiceCount(), foundAgents, fileNames)
	// One for each agent + one for the traffic manager
	require.Equal(s.ServiceCount()+1, yamlCount, fileNames)
}

func (s *multipleInterceptsSuite) TestGatherLogs_ManagerOnly() {
	require := s.Require()
	outputDir := s.T().TempDir()
	ctx := s.Context()
	outputFile := filepath.Join(outputDir, "allLogs.zip")
	s.cleanLogDir(ctx)
	itest.TelepresenceOk(ctx, "gather-logs", "--output-file", outputFile, "--get-pod-yaml", "--traffic-agents=None")
	foundManager, foundAgents, yamlCount, fileNames := s.getZipData(outputFile)
	require.True(foundManager)
	require.Equal(0, foundAgents, fileNames)
	require.Equal(1, yamlCount, fileNames)
}

func (s *multipleInterceptsSuite) TestGatherLogs_AgentsOnly() {
	require := s.Require()
	outputDir := s.T().TempDir()
	ctx := s.Context()
	outputFile := filepath.Join(outputDir, "allLogs.zip")
	s.cleanLogDir(ctx)
	itest.TelepresenceOk(ctx, "gather-logs", "--output-file", outputFile, "--get-pod-yaml", "--traffic-manager=False")
	foundManager, foundAgents, yamlCount, fileNames := s.getZipData(outputFile)
	require.False(foundManager)
	require.Equal(s.ServiceCount(), foundAgents, fileNames)
	require.Equal(s.ServiceCount(), yamlCount, fileNames)
}

func (s *multipleInterceptsSuite) TestGatherLogs_OneAgentOnly() {
	require := s.Require()
	outputDir := s.T().TempDir()
	ctx := s.Context()
	outputFile := filepath.Join(outputDir, "allLogs.zip")
	s.cleanLogDir(ctx)
	itest.TelepresenceOk(ctx, "gather-logs", "--output-file", outputFile, "--get-pod-yaml", "--traffic-manager=False", "--traffic-agents=hello-1")
	foundManager, foundAgents, yamlCount, fileNames := s.getZipData(outputFile)
	require.False(foundManager)
	require.Equal(1, foundAgents, fileNames)
	require.Equal(1, yamlCount, fileNames)
}

func (s *multipleInterceptsSuite) TestGatherLogs_NoPodYamlUnlessLogs() {
	require := s.Require()
	outputDir := s.T().TempDir()
	ctx := s.Context()
	outputFile := filepath.Join(outputDir, "allLogs.zip")
	s.cleanLogDir(ctx)
	itest.TelepresenceOk(ctx, "gather-logs", "--output-file", outputFile, "--get-pod-yaml", "--traffic-manager=False", "--traffic-agents=None")
	foundManager, foundAgents, yamlCount, fileNames := s.getZipData(outputFile)
	require.False(foundManager)
	require.Equal(0, foundAgents, fileNames)
	require.Equal(0, yamlCount, fileNames)
}

func (s *multipleInterceptsSuite) TestGatherLogs_NoK8sLogs() {
	require := s.Require()
	outputDir := s.T().TempDir()
	ctx := s.Context()
	outputFile := filepath.Join(outputDir, "allLogs.zip")
	s.cleanLogDir(ctx)
	itest.TelepresenceOk(ctx, "gather-logs", "--output-file", outputFile, "--get-pod-yaml", "--traffic-manager=False", "--traffic-agents=None")
	foundManager, foundAgents, yamlCount, fileNames := s.getZipData(outputFile)
	require.False(foundManager)
	require.Equal(0, foundAgents, fileNames)
	require.Equal(0, yamlCount, fileNames)
}

func (s *multipleInterceptsSuite) cleanLogDir(ctx context.Context) {
	require := s.Require()
	logDir, err := filelocation.AppUserLogDir(ctx)
	require.NoError(err)
	files, err := os.ReadDir(logDir)
	require.NoError(err)
	match := regexp.MustCompile(
		fmt.Sprintf(`^(:?traffic-manager-[0-9a-z-]+\.%s|hello-[0-%d]-[0-9a-z-]+\.%s)\.(:?log|yaml)$`,
			s.ManagerNamespace(), s.ServiceCount()-1, s.AppNamespace()))

	for _, file := range files {
		if match.MatchString(file.Name()) {
			dlog.Infof(ctx, "Deleting log-file %s", file.Name())
			require.NoError(os.Remove(filepath.Join(logDir, file.Name())))
		}
	}
}

func (s *multipleInterceptsSuite) getZipData(outputFile string) (bool, int, int, []string) {
	require := s.Require()
	zipReader, err := zip.OpenReader(outputFile)
	require.NoError(err)
	defer func() {
		s.NoError(zipReader.Close())
	}()
	// we collect and return the fileNames so that it makes it easier
	// to debug if tests fail
	helloMatch := regexp.MustCompile(fmt.Sprintf(`^hello-[0-%d]-[0-9a-z-]+\.%s\.(:?log|yaml)$`, s.ServiceCount()-1, s.AppNamespace()))
	tmMatch := regexp.MustCompile(fmt.Sprintf(`^traffic-manager-[0-9a-z-]+\.%s\.(:?log|yaml)$`, s.ManagerNamespace()))
	tmHdrMatch := regexp.MustCompile(`Traffic Manager v\d+\.\d+\.\d+`)
	agHdrMatch := regexp.MustCompile(`Traffic Agent v\d+\.\d+\.\d+`)
	foundManager, foundAgents, yamlCount := false, 0, 0
	fileNames := make([]string, len(zipReader.File))
	for i, f := range zipReader.File {
		fileNames[i] = f.Name
		if tmMatch.MatchString(f.Name) {
			if strings.HasSuffix(f.Name, ".yaml") {
				yamlCount++
				continue
			}
			foundManager = true
			fileContent := s.readZip(f)
			// We can be fairly certain we actually got a traffic-manager log
			// if we see the following
			require.Regexp(tmHdrMatch, string(fileContent))
		}
		if helloMatch.MatchString(f.Name) {
			if strings.HasSuffix(f.Name, ".yaml") {
				yamlCount++
				continue
			}
			foundAgents++
			fileContent := s.readZip(f)
			// We can be fairly certain we actually got a traffic-manager log
			// if we see the following
			require.Regexp(agHdrMatch, string(fileContent))
		}
	}
	return foundManager, foundAgents, yamlCount, fileNames
}

// readZip reads a zip file and returns the []byte string. Used in tests for
// checking that a zipped file's contents are correct. Exported since it is
// also used in telepresence_test.go
func (s *multipleInterceptsSuite) readZip(zippedFile *zip.File) []byte {
	fileReader, err := zippedFile.Open()
	s.Require().NoError(err)
	fileContent, err := io.ReadAll(fileReader)
	s.Require().NoError(err)
	return fileContent
}
