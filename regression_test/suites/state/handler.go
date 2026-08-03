package state

import (
	"encoding/json/v2"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/telepresenceio/telepresence/v2/pkg/proc"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/check"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/state"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// handlerMarkerTimeout bounds how long the Command handler's marker-file
// write, and the daemon's own delete-time termination of it, are given to
// take effect.
const handlerMarkerTimeout = 15 * time.Second

// Handler proves an intercept attachment's `command:` handler
// (pkg/client/cli/manifest/handler.go) sees TELEPRESENCE_INTERCEPT_ID,
// that its pid stays stable across a no-op apply,
// restarts on an argv change, and is killed by delete. sh-based, so it does
// not run on windows.
type Handler struct {
	rt.Suite
}

func init() {
	rt.Register(&Handler{},
		rt.InArea("state"),
		rt.NeedsManager(managers.Default),
		rt.NotOn("windows"),
	)
}

// handlerRecord mirrors handler.go's unexported handlerRec: the client-side
// record of a running handler process apply/delete reconcile against, read
// from handlerRecordPath.
type handlerRecord struct {
	Pid  int      `json:"pid"`
	Args []string `json:"args"`
}

// readHandlerRecord reads and unmarshals the handler record at path.
// startHandler (pkg/client/cli/manifest/handler.go) writes it synchronously
// before the apply invocation that started or restarted the handler
// returns, so no polling is needed here -- only the handler process's own
// marker-file write, checked separately, is asynchronous.
func readHandlerRecord(t testing.TB, path string) handlerRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading handler record %s: %v", path, err)
	}
	var rec handlerRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("unmarshal handler record %s: %v", path, err)
	}
	return rec
}

// handlerManifest returns a WorkstationState whose sole attachment is an
// intercept that starts a handler recording its TELEPRESENCE_INTERCEPT_ID
// into markerFile and then sleeping, so the test can observe both that the
// handler saw the attachment's environment and that it is still running.
// variant only changes the handler's argv (a no-op ":" marker), leaving the
// attachment's spec -- and hence drift detection -- untouched.
func handlerManifest(ns, markerFile, variant string) *state.State {
	ic := state.Intercept(stateInterceptWL)
	ic.Ports = []string{fmt.Sprintf("%d:http", stateLocalPort)}
	ic.Mount = &state.Mount{Enabled: state.Bool(false)}
	ic.Command = []string{"sh", "-c", fmt.Sprintf("echo $TELEPRESENCE_INTERCEPT_ID > %s; : %s; sleep 300", markerFile, variant)}

	conn := &state.Connection{
		Name:             stateConnName,
		Namespace:        ns,
		ManagerNamespace: managers.ManagerNamespace,
	}
	return &state.State{Connection: conn, Attachments: []state.Attachment{ic}}
}

// Test_ApplyHandlerCommand exercises the handler lifecycle the suite
// doc describes, in one pass.
func (s *Handler) Test_ApplyHandlerCommand() {
	t := s.T()
	ctx := s.Ctx()
	ns := s.AppNamespace()
	s.Workload(workloads.Echo(stateInterceptWL))
	defer quitAll(t, s.CLI(), ctx)

	dir := t.TempDir()
	markerFile := filepath.Join(dir, "handler-marker")
	recPath := handlerRecordPath(s.R(), stateInterceptWL)

	mf := writeManifest(t, s.R(), ctx, handlerManifest(ns, markerFile, "v1"))
	res := applyJSON(t, s.CLI(), ctx, mf, false)
	ia := res.attachment(t, stateInterceptWL)
	s.Equal("created", ia.Action)
	s.Equal("started", ia.Handler)

	check.EventuallyFile(t, markerFile, nonEmpty, handlerMarkerTimeout)
	interceptID := readTrimmed(t, markerFile)
	s.NotEmpty(interceptID)

	rec1 := readHandlerRecord(t, recPath)
	s.True(proc.IsAlive(rec1.Pid))

	// Second apply: attachment and handler are both unchanged, same pid.
	res = applyJSON(t, s.CLI(), ctx, mf, false)
	ia = res.attachment(t, stateInterceptWL)
	s.Equal("unchanged", ia.Action)
	s.Empty(ia.Handler)
	s.Equal(rec1.Pid, readHandlerRecord(t, recPath).Pid)

	// A changed command restarts the handler; the attachment itself stays
	// unchanged since the command has no server-side representation.
	s.Require().NoError(os.Remove(markerFile))
	mf2 := writeManifest(t, s.R(), ctx, handlerManifest(ns, markerFile, "v2"))
	res = applyJSON(t, s.CLI(), ctx, mf2, false)
	ia = res.attachment(t, stateInterceptWL)
	s.Equal("unchanged", ia.Action)
	s.Equal("restarted", ia.Handler)

	check.EventuallyFile(t, markerFile, nonEmpty, handlerMarkerTimeout)
	rec2 := readHandlerRecord(t, recPath)
	s.NotEqual(rec1.Pid, rec2.Pid)
	s.True(proc.IsAlive(rec2.Pid))
	s.False(proc.IsAlive(rec1.Pid))

	// Delete terminates the handler.
	res = deleteJSON(t, s.CLI(), ctx, mf2)
	s.Equal("stopped", res.attachment(t, stateInterceptWL).Handler)
	s.Eventually(func() bool { return !proc.IsAlive(rec2.Pid) },
		handlerMarkerTimeout, 200*time.Millisecond, "handler still alive after delete")
}

// nonEmpty is a check.EventuallyFile predicate accepting any non-blank
// content.
func nonEmpty(b []byte) bool {
	return len(strings.TrimSpace(string(b))) > 0
}

// readTrimmed reads path and returns its content with leading/trailing
// whitespace removed, failing t on a read error.
func readTrimmed(t testing.TB, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return strings.TrimSpace(string(b))
}
