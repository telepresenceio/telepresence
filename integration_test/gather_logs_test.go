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
	"time"

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

func (s *connectedSuite) TestGatherLogs_OnlyMappedLogs() {
	const svc = "echo"
	require := s.Require()
	defer func() {
		ctx := s.Context()
		itest.TelepresenceQuitOk(ctx)
		stdout := itest.TelepresenceOk(ctx, "connect", "--manager-namespace", s.ManagerNamespace())
		require.Contains(stdout, "Connected to context")
	}()

	ctx := s.Context()
	itest.TelepresenceDisconnectOk(ctx)

	otherOne := fmt.Sprintf("other-one-%s", s.Suffix())
	itest.CreateNamespaces(ctx, otherOne)
	defer itest.DeleteNamespaces(ctx, otherOne)

	otherTwo := fmt.Sprintf("other-two-%s", s.Suffix())
	itest.CreateNamespaces(ctx, otherTwo)
	defer itest.DeleteNamespaces(ctx, otherTwo)

	require.NoError(s.TelepresenceHelmInstall(itest.WithNamespaces(ctx, &itest.Namespaces{
		Namespace:         s.ManagerNamespace(),
		ManagedNamespaces: []string{otherOne, otherTwo},
	}), true))
	defer s.RollbackTM(ctx)

	itest.TelepresenceDisconnectOk(ctx)
	itest.ApplyEchoService(ctx, svc, otherOne, 8083)
	itest.ApplyEchoService(ctx, svc, otherTwo, 8084)

	itest.TelepresenceOk(ctx, "connect", "--manager-namespace", s.ManagerNamespace())

	itest.TelepresenceOk(ctx, "intercept", "--namespace", otherOne, "--mount", "false", svc)
	s.Eventually(
		func() bool {
			stdout := itest.TelepresenceOk(ctx, "list", "--namespace", otherOne, "--intercepts")
			return strings.Contains(stdout, svc+": intercepted")
		},
		10*time.Second,
		2*time.Second,
	)
	s.CapturePodLogs(ctx, fmt.Sprintf("app=%s", svc), "traffic-agent", otherOne)
	itest.TelepresenceOk(ctx, "leave", svc+"-"+otherOne)

	itest.TelepresenceOk(ctx, "intercept", "--namespace", otherTwo, "--mount", "false", svc)
	s.Eventually(
		func() bool {
			stdout := itest.TelepresenceOk(ctx, "list", "--namespace", otherTwo, "--intercepts")
			return strings.Contains(stdout, svc+": intercepted")
		},
		10*time.Second,
		2*time.Second,
	)
	s.CapturePodLogs(ctx, fmt.Sprintf("app=%s", svc), "traffic-agent", otherTwo)
	itest.TelepresenceOk(ctx, "leave", svc+"-"+otherTwo)

	bothNsRx := fmt.Sprintf("(?:%s|%s)", otherOne, otherTwo)
	outputDir := s.T().TempDir()
	outputFile := filepath.Join(outputDir, "allLogs.zip")
	cleanLogDir(ctx, require, bothNsRx, s.ManagerNamespace(), svc)
	itest.TelepresenceOk(ctx, "gather-logs", "--output-file", outputFile, "--traffic-manager=False")
	_, foundAgents, _, fileNames := getZipData(require, outputFile, bothNsRx, s.ManagerNamespace(), svc)
	require.Equal(2, foundAgents, fileNames)

	// Connect using mapped-namespaces
	itest.TelepresenceDisconnectOk(ctx)
	stdout := itest.TelepresenceOk(ctx, "connect", "--manager-namespace", s.ManagerNamespace(), "--mapped-namespaces", otherOne)
	require.Contains(stdout, "Connected to context")

	cleanLogDir(ctx, require, bothNsRx, s.ManagerNamespace(), svc)
	itest.TelepresenceOk(ctx, "list") // To ensure that the mapped namespaces are active
	itest.TelepresenceOk(ctx, "gather-logs", "--output-file", outputFile, "--traffic-manager=False")
	_, foundAgents, _, fileNames = getZipData(require, outputFile, bothNsRx, s.ManagerNamespace(), svc)
	require.Equal(1, foundAgents, fileNames)
}

func (s *multipleInterceptsSuite) cleanLogDir(ctx context.Context) {
	cleanLogDir(ctx, s.Require(), s.AppNamespace(), s.ManagerNamespace(), s.svcRegex())
}

func cleanLogDir(ctx context.Context, require *require.Assertions, nsRx, mgrNamespace, svcNameRx string) {
	logDir, err := filelocation.AppUserLogDir(ctx)
	require.NoError(err)
	files, err := os.ReadDir(logDir)
	require.NoError(err)
	match := regexp.MustCompile(
		fmt.Sprintf(`^(?:traffic-manager-[0-9a-z-]+\.%s|%s-[0-9a-z-]+\.%s)\.(?:log|yaml)$`,
			mgrNamespace, svcNameRx, nsRx))

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
			fileContent := readZip(require, f)
			// We can be fairly certain we actually got a traffic-manager log
			// if we see the following
			if tmHdrMatch.Match(fileContent) {
				foundManager = true
			}
		}
		if helloMatch.MatchString(f.Name) {
			if strings.HasSuffix(f.Name, ".yaml") {
				yamlCount++
				continue
			}
			fileContent := readZip(require, f)
			// We can be fairly certain we actually got a traffic-agent log
			// if we see the following
			if agHdrMatch.Match(fileContent) {
				foundAgents++
			}
		}
	}
	return foundManager, foundAgents, yamlCount, fileNames
}

// readZip reads a zip file and returns the []byte string. Used in tests for
// checking that a zipped file's contents are correct. Exported since it is
// also used in telepresence_test.go.
func readZip(require *require.Assertions, zippedFile *zip.File) []byte {
	fileReader, err := zippedFile.Open()
	require.NoError(err)
	fileContent, err := io.ReadAll(fileReader)
	require.NoError(err)
	return fileContent
}
