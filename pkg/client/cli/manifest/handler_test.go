package manifest

import (
	"context"
	"net/netip"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/blang/semver/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

// fakeUserClient is a minimal daemon.UserClient for tests that only exercise AddHandler and
// DaemonID; every other connector.ConnectorClient method panics if called.
type fakeUserClient struct {
	connector.ConnectorClient
	id       *daemon.Identifier
	handlers []string
}

func (f *fakeUserClient) Close() error                             { return nil }
func (f *fakeUserClient) Conn() *grpc.ClientConn                   { return nil }
func (f *fakeUserClient) Containerized() bool                      { return false }
func (f *fakeUserClient) DaemonPort() int                          { return -1 }
func (f *fakeUserClient) DaemonID() *daemon.Identifier             { return f.id }
func (f *fakeUserClient) Executable() string                       { return "" }
func (f *fakeUserClient) DaemonInfo() *daemon.Info                 { return nil }
func (f *fakeUserClient) Name() string                             { return f.id.Name }
func (f *fakeUserClient) Semver() semver.Version                   { return semver.Version{} }
func (f *fakeUserClient) SetConnectionInfo(string, string, string) {}

func (f *fakeUserClient) Lookup(context.Context, string) (netip.Addr, error) {
	return netip.Addr{}, nil
}

func (f *fakeUserClient) AddHandler(_ context.Context, id string, cmd *exec.Cmd, _ string) error {
	f.handlers = append(f.handlers, id)
	return nil
}

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx := t.Context()
	ctx = filelocation.WithAppUserCacheDir(ctx, t.TempDir())
	ctx = filelocation.WithAppUserLogDir(ctx, t.TempDir())
	ctx = daemon.WithUserClient(ctx, &fakeUserClient{id: &daemon.Identifier{Name: "test-daemon"}})
	return ctx
}

func TestHandlerStateFile_RoundTrip(t *testing.T) {
	ctx := testContext(t)
	a := &Attachment{Name: "echo-easy", Command: []string{"node", "server.js"}}

	rec := handlerRec{Pid: 4242, Args: a.Command}
	require.NoError(t, cache.SaveToUserCache(ctx, rec, handlerStateFile(ctx, a.Name), cache.Private))

	var got handlerRec
	require.NoError(t, cache.LoadFromUserCache(ctx, &got, handlerStateFile(ctx, a.Name)))
	assert.Equal(t, rec, got)

	require.NoError(t, cache.DeleteFromUserCache(ctx, handlerStateFile(ctx, a.Name)))
	assert.Error(t, cache.LoadFromUserCache(ctx, &got, handlerStateFile(ctx, a.Name)))
}

func TestStartStopHandler(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("requires sh")
	}
	ctx := testContext(t)
	a := &Attachment{Name: "echo-easy", Command: []string{"sh", "-c", "sleep 5"}}

	require.NoError(t, startHandler(ctx, a, map[string]string{"FOO": "bar"}, "echo-easy-id"))

	running, argsEqual, rec := handlerStatus(ctx, a)
	require.NotNil(t, rec)
	assert.True(t, running)
	assert.True(t, argsEqual)
	assert.Equal(t, a.Command, rec.Args)

	// stopHandler terminates the live process and removes the state file.
	assert.True(t, stopHandler(ctx, a))

	running, _, rec = handlerStatus(ctx, a)
	assert.False(t, running)
	assert.Nil(t, rec)

	// stopping an already-stopped handler is a silent no-op
	assert.False(t, stopHandler(ctx, a))
}

func TestHandlerStatus_ArgsDiffer(t *testing.T) {
	ctx := testContext(t)
	a := &Attachment{Name: "echo-easy", Command: []string{"node", "server.js"}}
	rec := handlerRec{Pid: 999999, Args: []string{"node", "other.js"}}
	require.NoError(t, cache.SaveToUserCache(ctx, rec, handlerStateFile(ctx, a.Name), cache.Private))

	running, argsEqual, got := handlerStatus(ctx, a)
	require.NotNil(t, got)
	assert.False(t, running) // pid 999999 is not expected to be alive
	assert.False(t, argsEqual)
}

func TestHandlerStateFile_SanitizesName(t *testing.T) {
	ctx := testContext(t)
	assert.Equal(t,
		filepath.Join("handlers", "daemon.json", "web_app.json"),
		handlerStateFile(ctx, "web/app"))
}
