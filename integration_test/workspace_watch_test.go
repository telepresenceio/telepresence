package integration_test

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	argorollouts "github.com/datawire/argo-rollouts-go-client/pkg/client/clientset/versioned"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/k8sapi/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/dnet"
)

func (s *notConnectedSuite) Test_WorkspaceListener() {
	rq := s.Require()
	ctx, cancel := context.WithCancel(s.Context())
	defer cancel()

	conn, err := s.trafficManagerConnection(ctx)
	rq.NoError(err)
	defer conn.Close()

	client := manager.NewManagerClient(conn)

	// Retrieve the session info from the traffic-manager. This is how
	// a connection to a namespace is made. The traffic-manager now
	// associates the returned session with that namespace in subsequent
	// calls.
	clientSession, err := client.ArriveAsClient(ctx, &manager.ClientInfo{
		Name:      "telepresence@datawire.io",
		Namespace: s.AppNamespace(),
		InstallId: "xxx",
		Product:   "telepresence",
		Version:   s.TelepresenceVersion(),
	})
	rq.NoError(err)

	// Normal ticker routine to keep the client alive.
	go func() {
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				_, _ = client.Remain(ctx, &manager.RemainRequest{Session: clientSession})
			case <-ctx.Done():
				_, _ = client.Depart(ctx, clientSession)
				return
			}
		}
	}()

	// Perform some actions that will generate events. Here:
	// 1. Create a deployment
	// 2. Prepare an intercept on that deployment (injects the traffic-agent into the pod)
	// 3. Create an intercept (changes state to INTERCEPTED)
	// 4. Leave the intercept (state goes back to INSTALLED)
	// 5. Remove the deployment
	go func() {
		defer cancel()
		s.ApplyApp(ctx, "echo-easy", "deploy/echo-easy")
		ir := &manager.CreateInterceptRequest{
			Session: clientSession,
			InterceptSpec: &manager.InterceptSpec{
				Name:         "echo-easy",
				Client:       "telepresence@datawire.io",
				Agent:        "echo-easy",
				WorkloadKind: "Deployment",
				Namespace:    s.AppNamespace(),
				Mechanism:    "tcp",
				TargetHost:   "127.0.0.1",
				TargetPort:   8080,
			},
		}
		pi, err := client.PrepareIntercept(ctx, ir)
		if !s.NoError(err) {
			return
		}
		spec := ir.InterceptSpec
		spec.ServiceName = pi.ServiceName
		spec.ServicePort = pi.ServicePort
		spec.ServicePortName = pi.ServicePortName
		spec.ServiceUid = pi.ServiceUid
		if pi.ServiceUid != "" {
			if pi.ServicePortName != "" {
				spec.PortIdentifier = pi.ServicePortName
			} else {
				spec.PortIdentifier = strconv.Itoa(int(pi.ServicePort))
			}
		} else {
			spec.PortIdentifier = strconv.Itoa(int(pi.ContainerPort))
		}
		_, err = client.CreateIntercept(ctx, ir)
		if !s.NoError(err) {
			return
		}
		time.Sleep(2 * time.Second)
		_, err = client.RemoveIntercept(ctx, &manager.RemoveInterceptRequest2{
			Session: clientSession,
			Name:    spec.Name,
		})
		s.NoError(err)
		time.Sleep(2 * time.Second)
		s.DeleteSvcAndWorkload(ctx, "deploy", "echo-easy")
		time.Sleep(2 * time.Second)
	}()

	wwStream, err := client.WatchWorkloads(ctx, &manager.WorkloadEventsRequest{
		SessionInfo: clientSession,
	})
	rq.NoError(err)

	// This map contains a key for each expected event from the workload watcher
	expectations := map[string]bool{
		"added":                 false,
		"progressing":           false,
		"available":             false,
		"agent installed":       false,
		"agent intercepted":     false,
		"agent installed again": false,
		"deleted":               false,
	}

	var interceptingClient string
	for {
		delta, err := wwStream.Recv()
		if err != nil {
			dlog.Infof(ctx, "watcher ended with %v", err)
			break
		}
		for _, ev := range delta.Events {
			dlog.Infof(ctx, "watcher event: %s %v", ev.Type, ev.Workload)
			switch ev.Type {
			case manager.WorkloadEvent_ADDED_UNSPECIFIED:
				expectations["added"] = true
				switch ev.Workload.State {
				case manager.WorkloadInfo_PROGRESSING:
					expectations["progressing"] = true
				case manager.WorkloadInfo_AVAILABLE:
					expectations["available"] = true
				}
			case manager.WorkloadEvent_MODIFIED:
				switch ev.Workload.State {
				case manager.WorkloadInfo_PROGRESSING:
					expectations["progressing"] = true
				case manager.WorkloadInfo_AVAILABLE:
					expectations["available"] = true
				}
				switch ev.Workload.AgentState {
				case manager.WorkloadInfo_INSTALLED:
					if expectations["agent intercepted"] {
						expectations["agent installed again"] = true
					} else {
						expectations["agent installed"] = true
					}
				case manager.WorkloadInfo_INTERCEPTED:
					expectations["agent intercepted"] = true
					if ics := ev.Workload.InterceptClients; len(ics) == 1 {
						interceptingClient = ics[0].Client
					}
				}
			case manager.WorkloadEvent_DELETED:
				expectations["deleted"] = true
			}
		}
	}
	for k, expect := range expectations {
		s.True(expect, k)
	}
	s.Equal("telepresence@datawire.io", interceptingClient)
}

func (s *notConnectedSuite) trafficManagerConnection(ctx context.Context) (*grpc.ClientConn, error) {
	itest.KubeConfig(ctx)
	cfg, err := clientcmd.BuildConfigFromFlags("", itest.KubeConfig(ctx))
	if err != nil {
		return nil, err
	}
	return dialTrafficManager(ctx, cfg, s.ManagerNamespace())
}

func dialTrafficManager(ctx context.Context, cfg *rest.Config, managerNamespace string) (*grpc.ClientConn, error) {
	k8sApi, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}

	argoRollouApi, err := argorollouts.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}

	ctx = k8sapi.WithJoinedClientSetInterface(ctx, k8sApi, argoRollouApi)
	dialer, err := dnet.NewK8sPortForwardDialer(ctx, cfg, k8sApi)
	if err != nil {
		return nil, err
	}
	return grpc.NewClient(fmt.Sprintf(dnet.K8sPFScheme+":///svc/traffic-manager.%s:8081", managerNamespace),
		grpc.WithResolvers(dnet.NewResolver(ctx)),
		grpc.WithContextDialer(dialer.Dial),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
}
