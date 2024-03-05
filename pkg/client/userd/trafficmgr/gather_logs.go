package trafficmgr

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	typed "k8s.io/client-go/kubernetes/typed/core/v1"
	"sigs.k8s.io/yaml"

	"github.com/datawire/dlib/dlog"
	"github.com/datawire/k8sapi/pkg/k8sapi"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
	"github.com/telepresenceio/telepresence/v2/pkg/agentmap"
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
		dlog.Error(ctx, err)
		result.Store(podLog, err.Error())
		return
	}
	defer logStream.Close()

	f, err := os.Create(filepath.Join(exportDir, podLog))
	if err != nil {
		dlog.Error(ctx, err)
		result.Store(podLog, err.Error())
		return
	}
	defer f.Close()

	if _, err = io.Copy(f, logStream); err != nil {
		err = fmt.Errorf("failed writing log to buffer: %w", err)
		dlog.Error(ctx, err)
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
			dlog.Error(ctx, err)
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

func (s *session) ForeachAgentPod(ctx context.Context, fn func(context.Context, typed.PodInterface, *core.Pod), filter func(*core.Pod) bool) error {
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

	coreAPI := k8sapi.GetK8sInterface(ctx).CoreV1()
	for _, ns := range s.GetCurrentNamespaces(true) {
		podsAPI := coreAPI.Pods(ns)
		podList, err := podsAPI.List(ctx, meta.ListOptions{})
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
				fn(ctx, podsAPI, pod)
			}(pod)
		}
		wg.Wait()
	}

	return nil
}

// GatherLogs acquires the logs for the traffic-manager and/or traffic-agents specified by the
// connector.LogsRequest and returns them to the caller.
func (s *session) GatherLogs(ctx context.Context, request *connector.LogsRequest) (*connector.LogsResponse, error) {
	exportDir := request.ExportDir
	coreAPI := k8sapi.GetK8sInterface(ctx).CoreV1()
	resp := &connector.LogsResponse{}
	result := sync.Map{}

	if !strings.EqualFold(request.Agents, "none") {
		err := s.ForeachAgentPod(ctx, func(ctx context.Context, podsAPI typed.PodInterface, pod *core.Pod) {
			podAndNs := fmt.Sprintf("%s.%s", pod.Name, pod.Namespace)
			dlog.Debugf(ctx, "gathering logs for %s, yaml = %t", podAndNs, request.GetPodYaml)
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
		ns := s.GetManagerNamespace()
		podsAPI := coreAPI.Pods(ns)
		selector := labels.SelectorFromSet(labels.Set{
			"app":          "traffic-manager",
			"telepresence": "manager",
		})
		podList, err := podsAPI.List(ctx, meta.ListOptions{LabelSelector: selector.String()})
		switch {
		case err != nil:
			err = fmt.Errorf("failed to gather logs for traffic manager in namespace %s: %w", ns, err)
			dlog.Error(ctx, err)
			resp.Error = err.Error()
		case len(podList.Items) == 1:
			pod := &podList.Items[0]
			podAndNs := fmt.Sprintf("%s.%s", pod.Name, ns)
			dlog.Debugf(ctx, "gathering logs for %s, yaml = %t", podAndNs, request.GetPodYaml)
			getPodLog(ctx, exportDir, &result, podsAPI, pod, "traffic-manager", request.GetPodYaml, false)
		case len(podList.Items) > 1:
			err = fmt.Errorf("multiple traffic managers found in namespace %s using selector %s", ns, selector.String())
			dlog.Error(ctx, err)
			resp.Error = err.Error()
		default:
			err := fmt.Errorf("no traffic manager found in namespace %s using selector %s", ns, selector.String())
			dlog.Error(ctx, err)
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
