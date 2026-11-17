package trafficmgr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"google.golang.org/grpc"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/telepresenceio/clog"
	rootdRpc "github.com/telepresenceio/telepresence/rpc/v2/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/authenticator"
	"github.com/telepresenceio/telepresence/v2/pkg/authenticator/patcher"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/json"
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

// newTokenFileCredentials returns credentials that read the bearer token from a file each time they are asked.
func newTokenFileCredentials(path string) *rootCredentials {
	return &rootCredentials{run: func(context.Context, *clientcmdapi.ExecConfig) ([]byte, error) {
		return tokenFileCredential(path)
	}}
}

// tokenFileCredential returns the content of the token file as an ExecCredential without expiry time.
func tokenFileCredential(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("unable to read the token file: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if token == "" {
		return nil, errors.New("the token file is empty")
	}
	return json.Marshal(map[string]any{
		"apiVersion": "client.authentication.k8s.io/v1",
		"kind":       "ExecCredential",
		"status":     map[string]string{"token": token},
	})
}

// rootKubeconfig returns the kubeconfig for the root daemon, and the credentials of the user of the context
// when they come from a credential plugin or a token file. The credentials are nil otherwise.
func rootKubeconfig(ctx context.Context, config *k8s.Kubeconfig, kubeContext string) ([]byte, *rootCredentials, error) {
	loader, err := config.OverridingConfig()
	if err != nil {
		return nil, nil, err
	}
	rk, err := patcher.CreateRootKubeConfig(ctx, loader, kubeContext)
	if err != nil {
		return nil, nil, err
	}
	switch {
	case rk.Exec != nil:
		return rk.Data, newRootCredentials(rk.Exec), nil
	case rk.TokenFile != "":
		return rk.Data, newTokenFileCredentials(rk.TokenFile), nil
	}
	return rk.Data, nil, nil
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
