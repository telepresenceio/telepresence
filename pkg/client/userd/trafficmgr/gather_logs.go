package trafficmgr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"google.golang.org/grpc"
	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	typed "k8s.io/client-go/kubernetes/typed/core/v1"
	"sigs.k8s.io/yaml"

	"github.com/telepresenceio/clog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/k8s"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

// getPodLog obtains the log and optionally the YAML for a given pod and stores it in
// a file named <POD NAME>.<POD NAMESPACE>.log (and .yaml, if applicable) under the
// given exportDir directory. An entry with the relative filename as a key is created
// in the result map. The entry will either contain the string "ok" or an error when
// the log or yaml for some reason could not be written to the file.
func getPodLog(ctx context.Context, exportDir string, result *sync.Map, podsAPI typed.PodInterface, pod *core.Pod, container string, podYAML, agent bool) {
	if !agentmap.IsPodRunning(pod) || agent && agentmap.AgentContainer(pod) == nil {
		return
	}
	podLog := pod.Name + "." + pod.Namespace + ".log"
	req := podsAPI.GetLogs(pod.Name, &core.PodLogOptions{Container: container})
	logStream, err := req.Stream(ctx)
	if err != nil {
		err = fmt.Errorf("failed to get log for %s.%s: %w", pod.Name, pod.Namespace, err)
		clog.Error(ctx, err)
		result.Store(podLog, err.Error())
		return
	}
	defer logStream.Close()

	f, err := os.Create(filepath.Join(exportDir, podLog))
	if err != nil {
		clog.Error(ctx, err)
		result.Store(podLog, err.Error())
		return
	}
	defer f.Close()

	if _, err = io.Copy(f, logStream); err != nil {
		err = fmt.Errorf("failed writing log to buffer: %w", err)
		clog.Error(ctx, err)
		result.Store(podLog, err.Error())
		return
	}
	result.Store(podLog, "ok")

	// Get the pod yaml if the user asked for it
	if podYAML {
		var b []byte
		podYaml := pod.Name + "." + pod.Namespace + ".yaml"
		if b, err = yaml.Marshal(pod); err != nil {
			err = fmt.Errorf("failed marshaling pod yaml: %w", err)
			clog.Error(ctx, err)
			result.Store(podYaml, err.Error())
			return
		}
		if err = os.WriteFile(filepath.Join(exportDir, podYaml), b, 0o666); err != nil {
			result.Store(podYaml, err.Error())
			return
		}
		result.Store(podYaml, "ok")
	}
}

func (s *session) foreachAgentPod(fn func(typed.PodInterface, *core.Pod), filter func(*core.Pod) bool) error {
	hasContainer := func(pod *core.Pod) bool {
		if filter == nil || filter(pod) {
			cns := pod.Spec.Containers
			for c := range cns {
				if cns[c].Name == agentconfig.ContainerName {
					return true
				}
			}
		}
		return false
	}

	coreAPI := k8sapi.GetK8sInterface(s).CoreV1()
	for _, ns := range s.GetCurrentNamespaces(true) {
		podsAPI := coreAPI.Pods(ns)
		podList, err := podsAPI.List(s, meta.ListOptions{})
		if err != nil {
			return err
		}
		pods := podList.Items
		podsWithContainer := make([]*core.Pod, 0, len(pods))
		for i := range pods {
			pod := &pods[i]
			if hasContainer(pod) {
				podsWithContainer = append(podsWithContainer, pod)
			}
		}
		wg := sync.WaitGroup{}
		wg.Add(len(podsWithContainer))
		for _, pod := range podsWithContainer {
			go func(pod *core.Pod) {
				defer wg.Done()
				fn(podsAPI, pod)
			}(pod)
		}
		wg.Wait()
	}

	return nil
}

// GatherLogs acquires the logs for the traffic-manager and/or traffic-agents specified by the
// connector.LogsRequest and returns them to the caller. It uses the manager's StreamLogs RPC
// when the connected manager supports it, and falls back to reading pod logs directly through
// the Kubernetes API against older managers.
func (s *session) GatherLogs(ctx context.Context, request *connector.LogsRequest) (*connector.LogsResponse, error) {
	exportDir := filepath.Join(filelocation.AppUserCacheDir(ctx), request.ExportDir)
	if s.managerSupportsStreamLogs() {
		return gatherLogsViaStream(ctx, s.ManagerClient(), s.SessionInfo(), exportDir, request)
	}
	if client.GetConfig(s).Cluster().UsesExternalManager() {
		return nil, errcat.User.New("the traffic-manager does not support the StreamLogs RPC (upgrade required); " +
			"direct log collection through the Kubernetes API is unavailable over this external connection")
	}
	return s.gatherLogsDirect(ctx, exportDir, request)
}

// gatherLogsDirect acquires the logs for the traffic-manager and/or traffic-agents specified
// by the connector.LogsRequest by listing pods and reading pods/log through the Kubernetes
// API. It is the path used against a traffic-manager that does not implement StreamLogs.
func (s *session) gatherLogsDirect(ctx context.Context, exportDir string, request *connector.LogsRequest) (*connector.LogsResponse, error) {
	coreAPI := k8sapi.GetK8sInterface(ctx).CoreV1()
	resp := &connector.LogsResponse{}
	result := sync.Map{}

	if !strings.EqualFold(request.Agents, "none") {
		err := s.foreachAgentPod(func(podsAPI typed.PodInterface, pod *core.Pod) {
			podAndNs := fmt.Sprintf("%s.%s", pod.Name, pod.Namespace)
			clog.Debugf(ctx, "gathering logs for %s, yaml = %t", podAndNs, request.GetPodYaml)
			getPodLog(ctx, exportDir, &result, podsAPI, pod, agentconfig.ContainerName, request.GetPodYaml, true)
		}, func(pod *core.Pod) bool {
			return strings.EqualFold(request.Agents, "all") || strings.Contains(pod.Name, request.Agents)
		})
		if err != nil {
			resp.Error = err.Error()
			return resp, nil
		}
	}

	// We want to get the traffic-manager log *last* so that if we generate
	// any errors in the traffic-manager getting the traffic-agent pods, we
	// want those logs to appear in what we export
	if request.TrafficManager {
		ns := k8s.GetManagerNamespace(ctx)
		podsAPI := coreAPI.Pods(ns)
		selector := labels.SelectorFromSet(labels.Set{
			"app":          agentconfig.ManagerAppName,
			"telepresence": "manager",
		})
		podList, err := podsAPI.List(ctx, meta.ListOptions{LabelSelector: selector.String()})
		switch {
		case err != nil:
			err = fmt.Errorf("failed to gather logs for traffic manager in namespace %s: %w", ns, err)
			clog.Error(ctx, err)
			resp.Error = err.Error()
		case len(podList.Items) == 1:
			pod := &podList.Items[0]
			podAndNs := fmt.Sprintf("%s.%s", pod.Name, ns)
			clog.Debugf(ctx, "gathering logs for %s, yaml = %t", podAndNs, request.GetPodYaml)
			getPodLog(ctx, exportDir, &result, podsAPI, pod, agentconfig.ManagerAppName, request.GetPodYaml, false)
		case len(podList.Items) > 1:
			err = fmt.Errorf("multiple traffic managers found in namespace %s using selector %s", ns, selector.String())
			clog.Error(ctx, err)
			resp.Error = err.Error()
		default:
			err := fmt.Errorf("no traffic manager found in namespace %s using selector %s", ns, selector.String())
			clog.Error(ctx, err)
			resp.Error = err.Error()
		}
	}
	pi := make(map[string]string)
	result.Range(func(k, v any) bool {
		pi[k.(string)] = v.(string)
		return true
	})
	resp.PodInfo = pi
	return resp, nil
}

// logStreamer is the part of manager.ManagerClient that gatherLogsViaStream needs. Narrowing
// to this one method lets tests supply a fake manager client without stubbing the rest of the
// (large) manager.ManagerClient interface.
type logStreamer interface {
	StreamLogs(ctx context.Context, in *manager.StreamLogsRequest, opts ...grpc.CallOption) (grpc.ServerStreamingClient[manager.LogChunk], error)
}

// gatherLogsViaStream fetches logs via the manager's StreamLogs RPC, which multiplexes
// per-pod frames onto one stream; gatherLogChunks assembles them into the same file
// layout and result map that the direct Kubernetes path produces.
func gatherLogsViaStream(ctx context.Context, mc logStreamer, session *manager.SessionInfo, exportDir string, request *connector.LogsRequest) (*connector.LogsResponse, error) {
	resp := &connector.LogsResponse{}

	agents := request.Agents
	if strings.EqualFold(agents, "none") {
		// connector.LogsRequest uses "none" for "no agent logs wanted"; StreamLogsRequest
		// uses "" or "false" for the same thing.
		agents = "false"
	}
	stream, err := mc.StreamLogs(ctx, &manager.StreamLogsRequest{
		Session:        session,
		TrafficManager: request.TrafficManager,
		Agents:         agents,
		GetPodYaml:     request.GetPodYaml,
	})
	if err != nil {
		resp.Error = err.Error()
		return resp, nil
	}

	podInfo, err := gatherLogChunks(ctx, exportDir, stream)
	if err != nil {
		resp.Error = err.Error()
	}
	resp.PodInfo = podInfo
	return resp, nil
}

// logChunkReceiver is the part of grpc.ServerStreamingClient[manager.LogChunk] that
// gatherLogChunks needs. Narrowing to Recv lets tests feed it a scripted frame sequence
// without implementing the rest of grpc.ClientStream.
type logChunkReceiver interface {
	Recv() (*manager.LogChunk, error)
}

// podLogAssembly holds a pod's open log file and the error text to record for
// its result-map entry once the END frame arrives.
type podLogAssembly struct {
	file    *os.File
	errText string
}

// gatherLogChunks assembles LogChunk frames into one <pod>.<namespace>.log (and an
// optional .yaml) per pod, plus a result map. Frames from different pods interleave
// on the stream, but a given pod's own frames arrive in order: BEGIN, data/error, END.
func gatherLogChunks(ctx context.Context, exportDir string, stream logChunkReceiver) (map[string]string, error) {
	result := make(map[string]string)
	pods := make(map[string]*podLogAssembly)
	defer func() {
		// Pods still in the map saw no END frame, so the stream ended prematurely.
		for _, pl := range pods {
			if pl.file != nil {
				pl.file.Close()
			}
		}
	}()

	for {
		chunk, err := stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return result, nil
			}
			return result, err
		}

		key := chunk.PodName + "." + chunk.PodNamespace
		logFile := key + ".log"

		switch chunk.Frame {
		case manager.LogChunk_BEGIN:
			pl := &podLogAssembly{}
			f, err := os.Create(filepath.Join(exportDir, logFile))
			if err != nil {
				clog.Error(ctx, err)
				pl.errText = err.Error()
			} else {
				pl.file = f
			}
			pods[key] = pl

		case manager.LogChunk_END:
			if pl, ok := pods[key]; ok {
				if pl.file != nil {
					pl.file.Close()
				}
				if pl.errText != "" {
					result[logFile] = pl.errText
				} else {
					result[logFile] = "ok"
				}
				delete(pods, key)
			}

		default:
			pl := pods[key]
			switch p := chunk.Payload.(type) {
			case *manager.LogChunk_Data:
				if pl != nil && pl.file != nil {
					if _, werr := pl.file.Write(p.Data); werr != nil && pl.errText == "" {
						pl.errText = werr.Error()
					}
				}
			case *manager.LogChunk_Error:
				if pl != nil {
					pl.errText = p.Error
				} else {
					result[logFile] = p.Error
				}
			case *manager.LogChunk_PodYaml:
				yamlFile := key + ".yaml"
				if werr := os.WriteFile(filepath.Join(exportDir, yamlFile), p.PodYaml, 0o666); werr != nil {
					clog.Error(ctx, werr)
					result[yamlFile] = werr.Error()
				} else {
					result[yamlFile] = "ok"
				}
			}
		}
	}
}
