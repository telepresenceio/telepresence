package trafficmgr

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/blang/semver/v4"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

// TestManagerSupportsStreamLogs exercises the version gate that decides whether gather-logs
// consumes the manager's StreamLogs RPC or falls back to reading pod logs directly through the
// Kubernetes API.
func TestManagerSupportsStreamLogs(t *testing.T) {
	tests := []struct {
		name    string
		version string
		want    bool
	}{
		{"older major", "1.99.0", false},
		{"older minor", "2.31.9", false},
		{"exact", "2.32.0", true},
		{"newer patch", "2.32.1", true},
		{"newer minor", "2.33.0", true},
		{"newer major", "3.0.0", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &session{managerVersion: semver.MustParse(tt.version)}
			require.Equal(t, tt.want, s.managerSupportsStreamLogs())
		})
	}
}

// fakeLogChunkStream implements grpc.ServerStreamingClient[manager.LogChunk] by returning a
// scripted sequence of frames, one per Recv call, then err if set, else io.EOF.
type fakeLogChunkStream struct {
	chunks []*manager.LogChunk
	pos    int
	err    error
}

func (f *fakeLogChunkStream) Recv() (*manager.LogChunk, error) {
	if f.pos >= len(f.chunks) {
		if f.err != nil {
			return nil, f.err
		}
		return nil, io.EOF
	}
	c := f.chunks[f.pos]
	f.pos++
	return c, nil
}

func (f *fakeLogChunkStream) Header() (metadata.MD, error) { return nil, nil }
func (f *fakeLogChunkStream) Trailer() metadata.MD         { return nil }
func (f *fakeLogChunkStream) CloseSend() error             { return nil }
func (f *fakeLogChunkStream) Context() context.Context     { return context.Background() }
func (f *fakeLogChunkStream) SendMsg(m any) error          { return nil }
func (f *fakeLogChunkStream) RecvMsg(m any) error          { return nil }

// fakeManagerLogClient implements the logStreamer interface gatherLogsViaStream needs,
// recording the request it was called with and returning a scripted stream.
type fakeManagerLogClient struct {
	stream  *fakeLogChunkStream
	err     error
	lastReq *manager.StreamLogsRequest
}

func (f *fakeManagerLogClient) StreamLogs(_ context.Context, in *manager.StreamLogsRequest, _ ...grpc.CallOption) (grpc.ServerStreamingClient[manager.LogChunk], error) {
	f.lastReq = in
	if f.err != nil {
		return nil, f.err
	}
	return f.stream, nil
}

func begin(pod, ns string) *manager.LogChunk {
	return &manager.LogChunk{PodName: pod, PodNamespace: ns, Frame: manager.LogChunk_BEGIN}
}

func end(pod, ns string) *manager.LogChunk {
	return &manager.LogChunk{PodName: pod, PodNamespace: ns, Frame: manager.LogChunk_END}
}

func dataChunk(pod, ns string, data string) *manager.LogChunk {
	return &manager.LogChunk{PodName: pod, PodNamespace: ns, Payload: &manager.LogChunk_Data{Data: []byte(data)}}
}

func errChunk(pod, ns string, msg string) *manager.LogChunk {
	return &manager.LogChunk{PodName: pod, PodNamespace: ns, Payload: &manager.LogChunk_Error{Error: msg}}
}

func yamlChunk(pod, ns string, yaml string) *manager.LogChunk {
	return &manager.LogChunk{PodName: pod, PodNamespace: ns, Payload: &manager.LogChunk_PodYaml{PodYaml: []byte(yaml)}}
}

// TestGatherLogChunks_Assembly scripts interleaved multi-pod frames -- one pod completes
// normally, one fails partway through, one includes a manifest -- and asserts the resulting
// file layout and result map.
func TestGatherLogChunks_Assembly(t *testing.T) {
	exportDir := t.TempDir()

	frames := []*manager.LogChunk{
		begin("agent-a", "default"),
		begin("agent-b", "default"),
		dataChunk("agent-a", "default", "hello "),
		dataChunk("agent-b", "default", "partial-log-"),
		dataChunk("agent-a", "default", "world"),
		end("agent-a", "default"),
		errChunk("agent-b", "default", "log truncated: exceeded 10MiB cap"),
		end("agent-b", "default"),
		begin("traffic-manager-0", "ambassador"),
		dataChunk("traffic-manager-0", "ambassador", "tm log line\n"),
		yamlChunk("traffic-manager-0", "ambassador", "apiVersion: v1\nkind: Pod\n"),
		end("traffic-manager-0", "ambassador"),
	}

	result, err := gatherLogChunks(context.Background(), exportDir, &fakeLogChunkStream{chunks: frames})
	require.NoError(t, err)

	require.Equal(t, map[string]string{
		"agent-a.default.log":               "ok",
		"agent-b.default.log":               "log truncated: exceeded 10MiB cap",
		"traffic-manager-0.ambassador.log":  "ok",
		"traffic-manager-0.ambassador.yaml": "ok",
	}, result)

	requireFileContent(t, exportDir, "agent-a.default.log", "hello world")
	// The pod's error does not discard data that arrived before it.
	requireFileContent(t, exportDir, "agent-b.default.log", "partial-log-")
	requireFileContent(t, exportDir, "traffic-manager-0.ambassador.log", "tm log line\n")
	requireFileContent(t, exportDir, "traffic-manager-0.ambassador.yaml", "apiVersion: v1\nkind: Pod\n")
}

// TestGatherLogChunks_ErrorBeforeAnyData covers a pod denied before it produced any log data:
// the BEGIN frame still creates the (empty) file, and the error frame is recorded as that
// file's result instead of "ok".
func TestGatherLogChunks_ErrorBeforeAnyData(t *testing.T) {
	exportDir := t.TempDir()

	frames := []*manager.LogChunk{
		begin("agent-c", "default"),
		errChunk("agent-c", "default", "denied by logs.telepresence.io SubjectAccessReview"),
		end("agent-c", "default"),
	}

	result, err := gatherLogChunks(context.Background(), exportDir, &fakeLogChunkStream{chunks: frames})
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		"agent-c.default.log": "denied by logs.telepresence.io SubjectAccessReview",
	}, result)
	requireFileContent(t, exportDir, "agent-c.default.log", "")
}

// TestGatherLogChunks_StreamError covers a failure of the underlying stream itself (as opposed
// to a per-pod error frame): it is reported back to the caller as an error rather than folded
// into the per-pod result map.
func TestGatherLogChunks_StreamError(t *testing.T) {
	exportDir := t.TempDir()
	origErr := errors.New("connection reset")
	recv := &erroringReceiver{after: []*manager.LogChunk{begin("agent-d", "default")}, err: origErr}
	_, err := gatherLogChunks(context.Background(), exportDir, recv)
	require.ErrorIs(t, err, origErr)
}

// erroringReceiver plays back a fixed sequence of frames and then fails with err instead of
// returning io.EOF, standing in for a broken connection mid-stream.
type erroringReceiver struct {
	after []*manager.LogChunk
	pos   int
	err   error
}

func (r *erroringReceiver) Recv() (*manager.LogChunk, error) {
	if r.pos < len(r.after) {
		c := r.after[r.pos]
		r.pos++
		return c, nil
	}
	return nil, r.err
}

func requireFileContent(t *testing.T, dir, name, want string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	require.NoError(t, err)
	require.Equal(t, want, string(b))
}

// TestGatherLogsViaStream_RequestMapping asserts connector.LogsRequest fields translate
// into StreamLogsRequest, including the "none" Agents sentinel becoming "false".
func TestGatherLogsViaStream_RequestMapping(t *testing.T) {
	exportDir := t.TempDir()
	session := &manager.SessionInfo{SessionId: "test-session"}

	fc := &fakeManagerLogClient{stream: &fakeLogChunkStream{}}
	resp, err := gatherLogsViaStream(context.Background(), fc, session, exportDir, &connector.LogsRequest{
		TrafficManager: true,
		Agents:         "None",
		GetPodYaml:     true,
	})
	require.NoError(t, err)
	require.Empty(t, resp.Error)
	require.Same(t, session, fc.lastReq.Session)
	require.True(t, fc.lastReq.TrafficManager)
	require.True(t, fc.lastReq.GetPodYaml)
	require.Equal(t, "false", fc.lastReq.Agents)
}

// TestGatherLogsViaStream_AgentsPassthrough asserts that a non-sentinel Agents value ("all" or
// a substring filter) passes through unchanged, since StreamLogsRequest interprets it the same
// way gatherLogsDirect's own client-side filter does.
func TestGatherLogsViaStream_AgentsPassthrough(t *testing.T) {
	exportDir := t.TempDir()
	session := &manager.SessionInfo{SessionId: "test-session"}

	fc := &fakeManagerLogClient{stream: &fakeLogChunkStream{}}
	_, err := gatherLogsViaStream(context.Background(), fc, session, exportDir, &connector.LogsRequest{
		Agents: "echo-easy",
	})
	require.NoError(t, err)
	require.Equal(t, "echo-easy", fc.lastReq.Agents)
}

// TestGatherLogsViaStream_ConnectError covers a failure to open the stream at all (e.g. the
// manager is unreachable or refuses the caller): it is returned as an error so GatherLogs can
// decide whether to fall back to the direct Kubernetes path.
func TestGatherLogsViaStream_ConnectError(t *testing.T) {
	exportDir := t.TempDir()
	session := &manager.SessionInfo{SessionId: "test-session"}

	fc := &fakeManagerLogClient{err: errors.New("manager unreachable")}
	resp, err := gatherLogsViaStream(context.Background(), fc, session, exportDir, &connector.LogsRequest{TrafficManager: true})
	require.Error(t, err)
	require.Contains(t, err.Error(), "manager unreachable")
	require.Nil(t, resp)
}

// TestGatherLogsViaStream_RefusedOnFirstRecv covers the manager refusing the
// caller outright: opening a gRPC client stream succeeds without waiting for
// the server, so the refusal surfaces on the first Recv, and with nothing
// produced it must be returned as an error for GatherLogs' fallback.
func TestGatherLogsViaStream_RefusedOnFirstRecv(t *testing.T) {
	exportDir := t.TempDir()
	session := &manager.SessionInfo{SessionId: "test-session"}

	refusal := status.Error(codes.Unauthenticated, "streaming logs requires an authenticated caller")
	fc := &fakeManagerLogClient{stream: &fakeLogChunkStream{err: refusal}}
	resp, err := gatherLogsViaStream(context.Background(), fc, session, exportDir, &connector.LogsRequest{TrafficManager: true})
	require.ErrorIs(t, err, refusal)
	require.Nil(t, resp)
}

// TestGatherLogsViaStream_MidStreamError covers a stream that fails after
// delivering data: the partial results are kept and the failure is reported
// in resp.Error rather than returned, so no fallback discards them.
func TestGatherLogsViaStream_MidStreamError(t *testing.T) {
	exportDir := t.TempDir()
	session := &manager.SessionInfo{SessionId: "test-session"}

	fc := &fakeManagerLogClient{stream: &fakeLogChunkStream{
		chunks: []*manager.LogChunk{
			begin("traffic-manager-0", "ambassador"),
			dataChunk("traffic-manager-0", "ambassador", "tm log line\n"),
			end("traffic-manager-0", "ambassador"),
		},
		err: errors.New("connection reset"),
	}}
	resp, err := gatherLogsViaStream(context.Background(), fc, session, exportDir, &connector.LogsRequest{TrafficManager: true})
	require.NoError(t, err)
	require.Contains(t, resp.Error, "connection reset")
	require.Equal(t, map[string]string{"traffic-manager-0.ambassador.log": "ok"}, resp.PodInfo)
	requireFileContent(t, exportDir, "traffic-manager-0.ambassador.log", "tm log line\n")
}

// TestIsStreamLogsAuthRefusal covers the classifier that decides whether a
// StreamLogs failure is an auth refusal the direct path may still satisfy.
func TestIsStreamLogsAuthRefusal(t *testing.T) {
	require.True(t, isStreamLogsAuthRefusal(status.Error(codes.Unauthenticated, "no principal")))
	require.True(t, isStreamLogsAuthRefusal(status.Error(codes.PermissionDenied, "denied")))
	require.False(t, isStreamLogsAuthRefusal(status.Error(codes.Unavailable, "down")))
	require.False(t, isStreamLogsAuthRefusal(errors.New("plain")))
}
