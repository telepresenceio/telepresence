package integration_test

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

const (
	smConnectionName = "state-manifest"
	smInterceptWL    = "echo-easy"
	smIngestWL       = "echo-two"
	smSvcPort        = 80
	smLocalPort      = 19080
)

type stateManifestSuite struct {
	itest.Suite
	itest.TrafficManager
}

func (s *stateManifestSuite) SuiteName() string {
	return "StateManifest"
}

func init() {
	itest.AddTrafficManagerSuite("", func(h itest.TrafficManager) itest.TestingSuite {
		return &stateManifestSuite{Suite: itest.Suite{Harness: h}, TrafficManager: h}
	})
}

func (s *stateManifestSuite) SetupSuite() {
	s.Suite.SetupSuite()
	ctx := s.Context()
	itest.ApplyEchoService(ctx, smInterceptWL, s.AppNamespace(), smSvcPort)
	itest.ApplyEchoService(ctx, smIngestWL, s.AppNamespace(), smSvcPort)
}

func (s *stateManifestSuite) TearDownSuite() {
	ctx := s.Context()
	s.DeleteSvcAndWorkload(ctx, "deploy", smInterceptWL)
	s.DeleteSvcAndWorkload(ctx, "deploy", smIngestWL)
	itest.TelepresenceQuitOk(ctx)
}

// attachmentsYAML renders the intercept+ingest attachment list shared by every manifest variant.
// The intercept's local port is the only field that a drift variant ever changes.
func (s *stateManifestSuite) attachmentsYAML(localPort int) string {
	return fmt.Sprintf(`attachments:
  - type: intercept
    name: %s
    ports: ["%d:%d"]
    mount:
      enabled: false
  - type: ingest
    name: %s
    mount:
      enabled: false
`, smInterceptWL, localPort, smSvcPort, smIngestWL)
}

// writeManifestWithConnection writes a manifest that declares the connection, using localPort for
// the intercept and, when mappedNS is set, a two-entry mappedNamespaces list. A namespace-scoped
// session maps exactly its own namespace, so the two-entry list can never be aligned with it,
// i.e. connection drift.
func (s *stateManifestSuite) writeManifestWithConnection(dir string, localPort int, mappedNS bool) string {
	conn := fmt.Sprintf("connection:\n  name: %s\n  namespace: %s\n  managerNamespace: %s\n",
		smConnectionName, s.AppNamespace(), s.ManagerNamespace())
	if mappedNS {
		conn += fmt.Sprintf("  mappedNamespaces: [%s, %s]\n", s.AppNamespace(), s.ManagerNamespace())
	}
	content := "apiVersion: telepresence.io/v1alpha1\nkind: WorkstationState\n" + conn + s.attachmentsYAML(localPort)
	name := fmt.Sprintf("m1-p%d-mn%v.yaml", localPort, mappedNS)
	return s.writeManifestFile(dir, name, content)
}

// writeManifestWithoutConnection writes a manifest with the same attachments but no connection
// block, meaning apply/delete depend on an already established connection.
func (s *stateManifestSuite) writeManifestWithoutConnection(dir string, localPort int) string {
	content := "apiVersion: telepresence.io/v1alpha1\nkind: WorkstationState\n" + s.attachmentsYAML(localPort)
	name := fmt.Sprintf("m2-p%d.yaml", localPort)
	return s.writeManifestFile(dir, name, content)
}

func (s *stateManifestSuite) writeManifestFile(dir, name, content string) string {
	path := filepath.Join(dir, name)
	s.Require().NoError(os.WriteFile(path, []byte(content), 0o644))
	return path
}

func (s *stateManifestSuite) interceptLine(action string) string {
	return fmt.Sprintf("intercept %s: %s", smInterceptWL, action)
}

func (s *stateManifestSuite) ingestLine(action string) string {
	return fmt.Sprintf("ingest %s: %s", smIngestWL, action)
}

func (s *stateManifestSuite) assertNothingRunning(status string) {
	s.Contains(status, "Root Daemon: Not running")
	s.Contains(status, "User Daemon: Not running")
}

func (s *stateManifestSuite) Test_ApplyDryRunConnectionMissing() {
	ctx := s.Context()
	dir := itest.TempDir(ctx)
	mf := s.writeManifestWithConnection(dir, smLocalPort, false)

	stdout := itest.TelepresenceOk(ctx, "apply", "--dry-run", "-f", mf)
	s.Contains(stdout, "connection: would-connect")
	s.Contains(stdout, s.interceptLine("would-create"))
	s.Contains(stdout, s.ingestLine("would-create"))

	s.assertNothingRunning(itest.TelepresenceOk(ctx, "status"))
}

func (s *stateManifestSuite) Test_ApplyCreatesReusesUnchanged() {
	ctx := s.Context()
	dir := itest.TempDir(ctx)
	mf := s.writeManifestWithConnection(dir, smLocalPort, false)
	defer itest.TelepresenceQuitOk(ctx)

	stdout := itest.TelepresenceOk(ctx, "apply", "-f", mf)
	s.Contains(stdout, "connection: connected")
	s.Contains(stdout, s.interceptLine("created"))
	s.Contains(stdout, s.ingestLine("created"))

	s.Contains(itest.TelepresenceOk(ctx, "list", "--intercepts"), smInterceptWL)

	stdout = itest.TelepresenceOk(ctx, "apply", "-f", mf)
	s.Contains(stdout, "connection: reused")
	s.Contains(stdout, s.interceptLine("unchanged"))
	s.Contains(stdout, s.ingestLine("unchanged"))

	stdout = itest.TelepresenceOk(ctx, "apply", "--dry-run", "-f", mf)
	s.Contains(stdout, "connection: reused")
	s.Contains(stdout, s.interceptLine("unchanged"))
	s.Contains(stdout, s.ingestLine("unchanged"))

	stdout = itest.TelepresenceOk(ctx, "delete", "-f", mf)
	s.Contains(stdout, "connection: disconnected")
}

func (s *stateManifestSuite) Test_ApplyAttachmentDrift() {
	ctx := s.Context()
	dir := itest.TempDir(ctx)
	mf := s.writeManifestWithConnection(dir, smLocalPort, false)
	driftMf := s.writeManifestWithConnection(dir, smLocalPort+1, false)
	defer itest.TelepresenceQuitOk(ctx)

	itest.TelepresenceOk(ctx, "apply", "-f", mf)

	stdout := itest.TelepresenceOk(ctx, "apply", "--dry-run", "-f", driftMf)
	s.Contains(stdout, s.interceptLine("would-re-create (drift:"))
	s.Contains(stdout, s.ingestLine("unchanged"))

	stdout = itest.TelepresenceOk(ctx, "apply", "-f", driftMf)
	s.Contains(stdout, s.interceptLine("re-created"))
	s.Contains(stdout, s.ingestLine("unchanged"))

	s.Contains(itest.TelepresenceOk(ctx, "list", "--intercepts"), smInterceptWL)

	itest.TelepresenceOk(ctx, "delete", "-f", driftMf)
}

func (s *stateManifestSuite) Test_ApplyConnectionDrift() {
	ctx := s.Context()
	dir := itest.TempDir(ctx)
	mf := s.writeManifestWithConnection(dir, smLocalPort, false)
	driftMf := s.writeManifestWithConnection(dir, smLocalPort, true)
	defer itest.TelepresenceQuitOk(ctx)

	itest.TelepresenceOk(ctx, "apply", "-f", mf)

	_, stderr, err := itest.Telepresence(ctx, "apply", "--dry-run", "-f", driftMf)
	s.Error(err)
	s.Contains(stderr, "has drifted from the manifest")

	_, stderr, err = itest.Telepresence(ctx, "apply", "-f", driftMf)
	s.Error(err)
	s.Contains(stderr, "has drifted from the manifest")

	// A drifted manifest must leave the connection and its attachments untouched.
	s.Contains(itest.TelepresenceOk(ctx, "list", "--intercepts"), smInterceptWL)

	itest.TelepresenceOk(ctx, "delete", "-f", mf)
}

func (s *stateManifestSuite) Test_Delete() {
	ctx := s.Context()
	dir := itest.TempDir(ctx)
	mf := s.writeManifestWithConnection(dir, smLocalPort, false)

	itest.TelepresenceOk(ctx, "apply", "-f", mf)

	stdout := itest.TelepresenceOk(ctx, "delete", "-f", mf)
	s.Contains(stdout, s.ingestLine("removed"))
	s.Contains(stdout, s.interceptLine("removed"))
	s.Contains(stdout, "connection: disconnected")
	s.Less(strings.Index(stdout, s.ingestLine("removed")), strings.Index(stdout, s.interceptLine("removed")))

	s.Contains(itest.TelepresenceOk(ctx, "status"), "Traffic Manager: Not connected")

	stdout = itest.TelepresenceOk(ctx, "delete", "-f", mf)
	s.Contains(stdout, "connection: not connected; nothing to tear down")

	itest.TelepresenceQuitOk(ctx)

	stdout = itest.TelepresenceOk(ctx, "delete", "-f", mf)
	s.Contains(stdout, "connection: not running; nothing to tear down")
}

func (s *stateManifestSuite) Test_DeleteAbsentAttachment() {
	ctx := s.Context()
	dir := itest.TempDir(ctx)
	mf := s.writeManifestWithConnection(dir, smLocalPort, false)
	defer itest.TelepresenceQuitOk(ctx)

	itest.TelepresenceOk(ctx, "apply", "-f", mf)
	itest.TelepresenceOk(ctx, "detach", smInterceptWL)

	stdout := itest.TelepresenceOk(ctx, "delete", "-f", mf)
	s.Contains(stdout, s.interceptLine("absent"))
	s.Contains(stdout, s.ingestLine("removed"))
	s.Contains(stdout, "connection: disconnected")
}

func (s *stateManifestSuite) Test_ApplyDeleteNoConnectionRequiresSession() {
	ctx := s.Context()
	dir := itest.TempDir(ctx)
	mf := s.writeManifestWithoutConnection(dir, smLocalPort)

	_, stderr, err := itest.Telepresence(ctx, "apply", "-f", mf)
	s.Error(err)
	s.Contains(stderr, `not connected; run "telepresence connect" first`)

	_, stderr, err = itest.Telepresence(ctx, "delete", "-f", mf)
	s.Error(err)
	s.Contains(stderr, `not connected; run "telepresence connect" first`)

	s.assertNothingRunning(itest.TelepresenceOk(ctx, "status"))
}

// handlerManifest writes a manifest whose intercept attachment declares a command that records
// its TELEPRESENCE_INTERCEPT_ID into markerFile and then sleeps, so the test can observe both
// that the handler saw the attachment's environment and that it's still running. variant only
// changes the handler's argv (a no-op ":" marker), leaving the attachment's spec, and hence drift
// detection, untouched.
func (s *stateManifestSuite) handlerManifest(dir, markerFile, variant string) string {
	content := fmt.Sprintf(`apiVersion: telepresence.io/v1alpha1
kind: WorkstationState
connection:
  name: %s
  namespace: %s
  managerNamespace: %s
attachments:
  - type: intercept
    name: %s
    ports: ["%d:%d"]
    mount:
      enabled: false
    command: ["sh", "-c", "echo $TELEPRESENCE_INTERCEPT_ID > %s; : %s; sleep 300"]
`, smConnectionName, s.AppNamespace(), s.ManagerNamespace(), smInterceptWL, smLocalPort, smSvcPort, markerFile, variant)
	name := fmt.Sprintf("handler-%s.yaml", variant)
	return s.writeManifestFile(dir, name, content)
}

// handlerRecord mirrors the client-side handler state file that "telepresence apply" writes
// under the user cache dir.
type handlerRecord struct {
	Pid  int      `json:"pid"`
	Args []string `json:"args"`
}

// handlerPid reads the recorded pid of the intercept attachment's handler process.
func (s *stateManifestSuite) handlerPid() int {
	var rec handlerRecord
	file := filepath.Join("handlers", daemon.InfoFileName, smInterceptWL+".json")
	s.Require().NoError(cache.LoadFromUserCache(s.Context(), &rec, file))
	return rec.Pid
}

func (s *stateManifestSuite) Test_ApplyHandlerCommand() {
	if runtime.GOOS == "windows" {
		s.T().Skip("handler command uses sh")
	}
	ctx := s.Context()
	dir := itest.TempDir(ctx)
	markerFile := filepath.Join(dir, "handler-marker")
	defer itest.TelepresenceQuitOk(ctx)

	mf := s.handlerManifest(dir, markerFile, "v1")
	stdout := itest.TelepresenceOk(ctx, "apply", "-f", mf)
	s.Contains(stdout, s.interceptLine("created")+", handler: started")

	var interceptID string
	s.Eventually(func() bool {
		b, err := os.ReadFile(markerFile)
		if err != nil || len(strings.TrimSpace(string(b))) == 0 {
			return false
		}
		interceptID = strings.TrimSpace(string(b))
		return true
	}, 15*time.Second, 200*time.Millisecond, "handler never wrote its marker file")
	s.NotEmpty(interceptID)

	pid1 := s.handlerPid()
	s.True(proc.IsAlive(pid1))

	// Second apply: attachment and handler are both unchanged, same pid.
	stdout = itest.TelepresenceOk(ctx, "apply", "-f", mf)
	s.Contains(stdout, s.interceptLine("unchanged"))
	s.NotContains(stdout, "handler:")
	s.Equal(pid1, s.handlerPid())

	// A changed command restarts the handler; the attachment itself stays unchanged since the
	// command has no server-side representation.
	os.Remove(markerFile)
	mf2 := s.handlerManifest(dir, markerFile, "v2")
	stdout = itest.TelepresenceOk(ctx, "apply", "-f", mf2)
	s.Contains(stdout, s.interceptLine("unchanged")+", handler: restarted")

	s.Eventually(func() bool {
		b, err := os.ReadFile(markerFile)
		return err == nil && len(strings.TrimSpace(string(b))) > 0
	}, 15*time.Second, 200*time.Millisecond, "restarted handler never wrote its marker file")
	pid2 := s.handlerPid()
	s.NotEqual(pid1, pid2)
	s.True(proc.IsAlive(pid2))
	s.False(proc.IsAlive(pid1))

	// Delete terminates the handler.
	itest.TelepresenceOk(ctx, "delete", "-f", mf2)
	s.Eventually(func() bool {
		return !proc.IsAlive(pid2)
	}, 15*time.Second, 200*time.Millisecond, "handler still alive after delete")
}

func (s *stateManifestSuite) Test_ApplyDeleteWithoutConnectionKeepsSession() {
	ctx := s.Context()
	dir := itest.TempDir(ctx)
	mf := s.writeManifestWithoutConnection(dir, smLocalPort)
	s.TelepresenceConnect(ctx)
	defer itest.TelepresenceQuitOk(ctx)

	stdout := itest.TelepresenceOk(ctx, "apply", "--dry-run", "-f", mf)
	s.NotContains(stdout, "connection:")
	s.Contains(stdout, s.interceptLine("would-create"))
	s.Contains(stdout, s.ingestLine("would-create"))

	stdout = itest.TelepresenceOk(ctx, "apply", "-f", mf)
	s.NotContains(stdout, "connection:")
	s.Contains(stdout, s.interceptLine("created"))
	s.Contains(stdout, s.ingestLine("created"))

	stdout = itest.TelepresenceOk(ctx, "apply", "-f", mf)
	s.Contains(stdout, s.interceptLine("unchanged"))
	s.Contains(stdout, s.ingestLine("unchanged"))

	stdout = itest.TelepresenceOk(ctx, "delete", "-f", mf)
	s.NotContains(stdout, "connection:")
	s.Contains(stdout, s.interceptLine("removed"))
	s.Contains(stdout, s.ingestLine("removed"))

	s.NotContains(itest.TelepresenceOk(ctx, "status"), "Traffic Manager: Not connected")
}
