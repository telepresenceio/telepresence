package commands

import (
	"compress/gzip"
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/protobuf/types/known/emptypb"
	corev1 "k8s.io/api/core/v1"
	typedv1 "k8s.io/client-go/kubernetes/typed/core/v1"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/common"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/trafficmgr"
	"github.com/telepresenceio/telepresence/v2/pkg/dnet"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

type traceCommand struct {
	command *cobra.Command
}

func (*traceCommand) group() string {
	return "Tracing"
}

func (c *traceCommand) cobraCommand(ctx context.Context) *cobra.Command {
	if c.command != nil {
		return c.command
	}

	var remotePort uint16
	var destFile string
	c.command = &cobra.Command{
		Use:  "gather-traces",
		Args: cobra.NoArgs,

		Short: "Gather Traces",
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.gatherTraces(cmd, remotePort, destFile)
		},
		Annotations: map[string]string{
			CommandRequiresSession: "true",
		},
	}
	c.command.Flags().Uint16VarP(&remotePort, "port", "p", 15766,
		"The remote port where traffic manager and agent are exposing traces."+
			"Corresponds to tracing.grpcPort in the helm chart values")
	c.command.Flags().StringVarP(&destFile, "output-file", "o", "./traces.gz", "The gzip to be created with binary trace data")

	return c.command
}

func (*traceCommand) init(_ context.Context) {}

func (*traceCommand) tracesFor(ctx context.Context, conn *grpc.ClientConn, ch chan []byte) error {
	daemonCli := common.NewTracingClient(conn)
	result, err := daemonCli.DumpTraces(ctx, &emptypb.Empty{})
	if err != nil {
		return err
	}
	data := result.GetTraceData()
	select {
	case ch <- data:
	case <-ctx.Done():
	}
	return nil
}

func (*traceCommand) launchTraceWriter(ctx context.Context, destFile string) (chan []byte, chan error, error) {
	ch := make(chan []byte)
	if !filepath.IsAbs(destFile) {
		wd := GetCwd(ctx)
		destFile = filepath.Join(wd, destFile)
	}
	file, err := os.Create(destFile)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create trace file: %w", err)
	}
	errCh := make(chan error)

	go func() {
		zipW := gzip.NewWriter(file)
		defer func() {
			err = zipW.Close()
			if err != nil {
				errCh <- err
				return
			}
			err = file.Close()
			if err != nil {
				errCh <- err
				return
			}
			close(errCh)
		}()

		for {
			select {
			case <-ctx.Done():
				return
			case data, ok := <-ch:
				if !ok {
					return
				}
				_, err := zipW.Write(data)
				if err != nil {
					errCh <- err
					return
				}
			}
		}
	}()
	return ch, errCh, nil
}

func (c *traceCommand) userdTraces(ctx context.Context, tCh chan []byte) error {
	userdConn, err := client.DialSocket(ctx, client.ConnectorSocketName)
	if err != nil {
		return err
	}
	defer userdConn.Close()

	err = c.tracesFor(ctx, userdConn, tCh)
	if err != nil {
		return err
	}
	return nil
}

func (c *traceCommand) rootdTraces(ctx context.Context, tCh chan []byte) error {
	dConn, err := client.DialSocket(ctx, client.DaemonSocketName)
	if err != nil {
		return err
	}
	defer dConn.Close()

	err = c.tracesFor(ctx, dConn, tCh)
	if err != nil {
		return err
	}
	return nil
}

func (c *traceCommand) trafficManagerTraces(ctx context.Context, tCh chan []byte, remotePort string) error {
	sess := trafficmgr.GetSession(ctx)
	kpf, err := dnet.NewK8sPortForwardDialer(ctx, sess.GetRestConfig(), k8sapi.GetK8sInterface(ctx))
	if err != nil {
		return err
	}
	grpcAddr := net.JoinHostPort(
		"svc/traffic-manager."+sess.GetManagerNamespace(),
		remotePort)
	tc, tCancel := context.WithTimeout(ctx, 20*time.Second)
	defer tCancel()

	opts := []grpc.DialOption{grpc.WithContextDialer(kpf),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithNoProxy(),
		grpc.WithBlock(),
		grpc.WithReturnConnectionError(),
	}

	var conn *grpc.ClientConn
	if conn, err = grpc.DialContext(tc, grpcAddr, opts...); err != nil {
		return err
	}
	err = c.tracesFor(ctx, conn, tCh)
	if err != nil {
		return err
	}
	return nil
}

func (c *traceCommand) agentTraces(cmd *cobra.Command, tCh chan []byte, remotePort string) error {
	ctx := cmd.Context()
	sess := trafficmgr.GetSession(ctx)
	kpf, err := dnet.NewK8sPortForwardDialer(ctx, sess.GetRestConfig(), k8sapi.GetK8sInterface(ctx))
	if err != nil {
		return err
	}
	return sess.ForeachAgentPod(ctx, func(ctx context.Context, pi typedv1.PodInterface, pod *corev1.Pod) {
		if err != nil {
			dlog.Warnf(ctx, "unable to get traces for %s.%s: %v", pod.Name, pod.Namespace, err)
			return
		}
		name := fmt.Sprintf("%s.%s", pod.Name, pod.Namespace)
		addr := net.JoinHostPort(name, remotePort)
		tc, tCancel := context.WithTimeout(ctx, 20*time.Second)
		defer tCancel()

		opts := []grpc.DialOption{grpc.WithContextDialer(kpf),
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithNoProxy(),
			grpc.WithBlock(),
			grpc.WithReturnConnectionError(),
		}

		var conn *grpc.ClientConn
		if conn, err = grpc.DialContext(tc, addr, opts...); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "Unable to dial %s: %v", name, err)
			return
		}
		defer conn.Close()
		err := c.tracesFor(tc, conn, tCh)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "Unable to get traces for %s: %v", name, err)
			return
		}
	}, nil)
}

func (c *traceCommand) gatherTraces(cmd *cobra.Command, remotePort uint16, destFile string) error {
	ctx := cmd.Context()
	port := strconv.FormatUint(uint64(remotePort), 10)

	tCh, errCh, err := c.launchTraceWriter(ctx, destFile)
	if err != nil {
		return err
	}

	wg := &sync.WaitGroup{}
	wg.Add(4)

	go func() {
		defer wg.Done()
		err := c.rootdTraces(ctx, tCh)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "failed to collect root daemon traces: %v", err)
		}
	}()

	go func() {
		defer wg.Done()
		err := c.userdTraces(ctx, tCh)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "failed to collect user daemon traces: %v", err)
		}
	}()

	go func() {
		defer wg.Done()
		err = c.trafficManagerTraces(ctx, tCh, port)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "failed to collect traffic-manager traces: %v", err)
		}
	}()

	go func() {
		defer wg.Done()
		err := c.agentTraces(cmd, tCh, port)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "failed to collect traffic agent traces: %v", err)
		}
	}()

	wg.Wait()

	close(tCh)
	err = <-errCh

	return err
}
