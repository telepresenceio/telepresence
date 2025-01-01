package integration_test

import (
	"context"
	"strconv"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func (s *notConnectedSuite) Test_WorkspaceListener() {
	s.Require().NoError(s.DoWithTrafficManager(s.Context(), func(ctx context.Context, cancel context.CancelFunc, client manager.ManagerClient, session *manager.SessionInfo) {
		rq := s.Require()

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
			if !s.NoError(err) {
				return
			}
			spec := ir.InterceptSpec
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
				Session: session,
				Name:    spec.Name,
			})
			s.NoError(err)
			time.Sleep(2 * time.Second)
			s.DeleteSvcAndWorkload(ctx, "deploy", "echo-easy")
			time.Sleep(2 * time.Second)
		}()

		wwStream, err := client.WatchWorkloads(ctx, &manager.WorkloadEventsRequest{
			SessionInfo: session,
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
						expectations["agent installed"] = true
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
	}))
}
