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

	"github.com/stretchr/testify/require"

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

func (s *singleServiceSuite) TestGatherLogs_OnlyMappedLogs() {
	require := s.Require()
	ctx := s.Context()
	otherNS := fmt.Sprintf("other-ns-%s", s.Suffix())
	itest.CreateNamespaces(ctx, otherNS)
	itest.ApplyEchoService(ctx, s.ServiceName(), otherNS, 8083)
	itest.TelepresenceOk(ctx, "intercept", "--namespace", otherNS, "--mount", "false", s.ServiceName())
	itest.TelepresenceOk(ctx, "leave", s.ServiceName()+"-"+otherNS)
	itest.TelepresenceOk(ctx, "intercept", "--namespace", s.AppNamespace(), "--mount", "false", s.ServiceName())
	itest.TelepresenceOk(ctx, "leave", s.ServiceName()+"-"+s.AppNamespace())

	bothNsRx := fmt.Sprintf("(?:%s|%s)", s.AppNamespace(), otherNS)
	outputDir := s.T().TempDir()
	outputFile := filepath.Join(outputDir, "allLogs.zip")
	cleanLogDir(ctx, require, bothNsRx, s.ManagerNamespace(), s.ServiceName())
	itest.TelepresenceOk(ctx, "gather-logs", "--output-file", outputFile, "--traffic-manager=False")
	_, foundAgents, _, fileNames := getZipData(require, outputFile, bothNsRx, s.ManagerNamespace(), s.ServiceName())
	require.Equal(2, foundAgents, fileNames)

	// Connect using mapped-namespaces
	itest.TelepresenceDisconnectOk(ctx)
	stdout := itest.TelepresenceOk(ctx, "connect", "--mapped-namespaces", s.AppNamespace())
	require.Contains(stdout, "Connected to context default")
	defer func() {
		itest.TelepresenceQuitOk(ctx)
		stdout := itest.TelepresenceOk(ctx, "connect")
		require.Contains(stdout, "Connected to context default")
	}()

	cleanLogDir(ctx, require, bothNsRx, s.ManagerNamespace(), s.ServiceName())
	itest.TelepresenceOk(ctx, "gather-logs", "--output-file", outputFile, "--traffic-manager=False")
	_, foundAgents, _, fileNames = getZipData(require, outputFile, bothNsRx, s.ManagerNamespace(), s.ServiceName())
	require.Equal(1, foundAgents, fileNames)
}

func (s *multipleInterceptsSuite) cleanLogDir(ctx context.Context) {
	cleanLogDir(ctx, s.Require(), s.AppNamespace(), s.ManagerNamespace(), s.svcRegex())
}

func cleanLogDir(ctx context.Context, require *require.Assertions, appNamespace, mgrNamespace, svcNameRx string) {
	logDir, err := filelocation.AppUserLogDir(ctx)
	require.NoError(err)
	files, err := os.ReadDir(logDir)
	require.NoError(err)
	match := regexp.MustCompile(
		fmt.Sprintf(`^(?:traffic-manager-[0-9a-z-]+\.%s|%s-[0-9a-z-]+\.%s)\.(?:log|yaml)$`,
			mgrNamespace, svcNameRx, appNamespace))

	for _, file := range files {
		if match.MatchString(file.Name()) {
			dlog.Infof(ctx, "Deleting log-file %s", file.Name())
			require.NoError(os.Remove(filepath.Join(logDir, file.Name())))
		}
	}
}

func (s *multipleInterceptsSuite) svcRegex() string {
	return fmt.Sprintf("hello-[0-%d]", s.ServiceCount())
}

func (s *multipleInterceptsSuite) getZipData(outputFile string) (bool, int, int, []string) {
	return getZipData(s.Require(), outputFile, s.AppNamespace(), s.ManagerNamespace(), s.svcRegex())
}

func getZipData(require *require.Assertions, outputFile, appNamespace, mgrNamespace, svcNameRx string) (bool, int, int, []string) {
	zipReader, err := zip.OpenReader(outputFile)
	require.NoError(err)
	defer func() {
		require.NoError(zipReader.Close())
	}()
	// we collect and return the fileNames so that it makes it easier
	// to debug if tests fail
	helloMatch := regexp.MustCompile(fmt.Sprintf(`^%s-[0-9a-z-]+\.%s\.(?:log|yaml)$`, svcNameRx, appNamespace))
	tmMatch := regexp.MustCompile(fmt.Sprintf(`^traffic-manager-[0-9a-z-]+\.%s\.(?:log|yaml)$`, mgrNamespace))
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
			fileContent := readZip(require, f)
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
			fileContent := readZip(require, f)
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
func readZip(require *require.Assertions, zippedFile *zip.File) []byte {
	fileReader, err := zippedFile.Open()
	require.NoError(err)
	fileContent, err := io.ReadAll(fileReader)
	require.NoError(err)
	return fileContent
}
