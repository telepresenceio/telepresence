package integration_test

import (
	"context"
	"strconv"
	"time"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

func (s *notConnectedSuite) createIntercept(ctx context.Context, client manager.ManagerClient, session *manager.SessionInfo) (*manager.InterceptInfo, error) {
	ir := &manager.CreateInterceptRequest{
		Session: session,
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
	if err != nil {
		return nil, err
	}
	spec := ir.InterceptSpec
	spec.ServicePort = pi.ServicePort
	spec.ServicePortName = pi.ServicePortName
	spec.ServiceUid = pi.ServiceUid
	spec.ContainerPort = pi.ContainerPort
	spec.Protocol = pi.Protocol
	spec.ContainerName = pi.ContainerName
	if pi.ServiceUid != "" {
		if pi.ServicePortName != "" {
			spec.PortIdentifier = pi.ServicePortName
		} else {
			spec.PortIdentifier = strconv.Itoa(int(pi.ServicePort))
		}
	} else {
		spec.PortIdentifier = strconv.Itoa(int(pi.ContainerPort))
	}
	return client.CreateIntercept(ctx, ir)
}

func (s *notConnectedSuite) Test_WorkloadListener() {
	if !s.ClientVersion().EQ(version.Structured) {
		s.T().Skip(`Not part of compatibility tests. DoWithTrafficManager assumes compiled executable`)
	}
	s.Require().NoError(s.DoWithTrafficManager(s.Context(), func(ctx context.Context, cancel context.CancelFunc, client manager.ManagerClient, session *manager.SessionInfo) {
		rq := s.Require()

		// Perform some actions that will generate events. Here:
		// 1. Create a deployment
		// 2. Prepare an intercept on that deployment (injects the traffic-agent into the pod)
		// 3. Create an intercept (changes state to INTERCEPTED)
		// 4. Leave the intercept (state goes back to INSTALLED)
		// 5. Remove the deployment
		defer cancel()
		_, err := client.SetLogLevel(ctx, &manager.LogLevelRequest{LogLevel: "trace"})
		if !s.NoError(err) {
			return
		}

		toCtx, toCancel := context.WithTimeout(ctx, time.Minute)
		defer toCancel()
		wwStream, err := client.WatchWorkloads(toCtx, &manager.WorkloadEventsRequest{
			SessionInfo: session,
		})
		rq.NoError(err)

		defer func() {
			_, _ = client.SetLogLevel(ctx, &manager.LogLevelRequest{LogLevel: "debug"})
		}()

		s.ApplyApp(ctx, "echo-easy", "deploy/echo-easy")

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

		var spec *manager.InterceptSpec
		var interceptingClient string
		for !(s.T().Failed() || expectations["deleted"]) {
			delta, err := wwStream.Recv()
			if err != nil {
				clog.Infof(ctx, "watcher ended with %v", err)
				break
			}
			for _, ev := range delta.Events {
				clog.Infof(ctx, "watcher event: %s %v", ev.Type, ev.Workload)
				switch ev.Type {
				case manager.WorkloadEvent_ADDED_UNSPECIFIED, manager.WorkloadEvent_MODIFIED:
					expectations["added"] = true
					switch ev.Workload.State {
					case manager.WorkloadInfo_PROGRESSING:
						expectations["progressing"] = true
					case manager.WorkloadInfo_AVAILABLE:
						if !expectations["available"] {
							expectations["available"] = true
							ii, err := s.createIntercept(ctx, client, session)
							if !s.NoError(err) {
								return
							}
							spec = ii.Spec
						}
						switch ev.Workload.AgentState {
						case manager.WorkloadInfo_INSTALLED:
							if expectations["agent intercepted"] {
								expectations["agent installed again"] = true
								s.DeleteSvcAndWorkload(ctx, "deploy", "echo-easy")
							} else {
								expectations["agent installed"] = true
							}
						case manager.WorkloadInfo_INTERCEPTED:
							expectations["agent installed"] = true
							expectations["agent intercepted"] = true
							if ics := ev.Workload.InterceptClients; len(ics) == 1 {
								interceptingClient = ics[0].Client
							}
							_, err = client.RemoveIntercept(ctx, &manager.RemoveInterceptRequest2{
								Session: session,
								Name:    spec.Name,
							})
							s.NoError(err)
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
	}))
}
