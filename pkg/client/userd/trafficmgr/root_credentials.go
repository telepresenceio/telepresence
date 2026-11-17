package trafficmgr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"google.golang.org/grpc"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/telepresenceio/clog"
	rootdRpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/authenticator"
)

// rootCredentials runs the credential plugin of the kubeconfig's user on behalf of the root daemon.
type rootCredentials struct {
	execConfig *clientcmdapi.ExecConfig

	// run executes the plugin. It is replaced by tests.
	run func(context.Context, *clientcmdapi.ExecConfig) ([]byte, error)
}

func newRootCredentials(ec *clientcmdapi.ExecConfig) *rootCredentials {
	return &rootCredentials{execConfig: ec, run: authenticator.ResolveExecConfig}
}

// credential runs the plugin and returns the ExecCredential JSON that it printed.
func (c *rootCredentials) credential(ctx context.Context) ([]byte, error) {
	clog.Debug(ctx, "root daemon credential request")
	return c.run(ctx, c.execConfig)
}

// serve answers the root daemon's credential requests on stream until the stream or ctx ends.
func (c *rootCredentials) serve(ctx context.Context, stream grpc.BidiStreamingClient[rootdRpc.CredentialReply, rootdRpc.CredentialRequest]) error {
	var (
		sendMu sync.Mutex
		wg     sync.WaitGroup
	)
	defer wg.Wait()
	for {
		req, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("credential stream failed: %w", err)
		}
		wg.Go(func() {
			reply := &rootdRpc.CredentialReply{Id: req.Id}
			if cred, err := c.credential(ctx); err != nil {
				reply.Error = err.Error()
			} else {
				reply.ExecCredential = cred
			}
			sendMu.Lock()
			err := stream.Send(reply)
			sendMu.Unlock()
			if err != nil && ctx.Err() == nil {
				clog.Errorf(ctx, "unable to answer credential request %d: %v", req.Id, err)
			}
		})
	}
}

// startRootCredentials opens the credential stream on rd and serves it in the background.
func (s *session) startRootCredentials(rd rootdRpc.DaemonClient) error {
	stream, err := rd.ProvideCredentials(s)
	if err != nil {
		return fmt.Errorf("failed to open the credential stream to the root daemon: %w", err)
	}
	go func() {
		if err := s.rootCreds.serve(s, stream); err != nil {
			clog.Debug(s, err)
		}
	}()
	return nil
}
