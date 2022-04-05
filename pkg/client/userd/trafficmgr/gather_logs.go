package trafficmgr

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	core "k8s.io/api/core/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	typed "k8s.io/client-go/kubernetes/typed/core/v1"
	"sigs.k8s.io/yaml"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/k8sapi"
)

// getPodLogs is a helper function for getting the logs from the container
// of a given pod. If we are unable to get a log for a given pod, we will
// instead return the error instead of the log, so that:
// - one failure doesn't prevent us from getting logs from other pods
// - it is easy to figure out why getting logs for a given pod failed
func getPodLog(ctx context.Context, podsAPI typed.PodInterface, pod *core.Pod, container string, podYAML bool) (string, string) {
	req := podsAPI.GetLogs(pod.Name, &core.PodLogOptions{Container: container})
	podLogs, err := req.Stream(ctx)
	if err != nil {
		msg := fmt.Sprintf("Failed to get log for %s.%s: %v", pod.Name, pod.Namespace, err)
		dlog.Error(ctx, msg)
		return msg, ""
	}
	defer podLogs.Close()

	buf := new(bytes.Buffer)
	if _, err = io.Copy(buf, podLogs); err != nil {
		msg := fmt.Sprintf("Failed writing log to buffer: %v", err)
		dlog.Error(ctx, msg)
		return msg, ""
	}
	podLog := buf.String()

	// Get the pod yaml if the user asked for it
	if podYAML {
		var b []byte
		if b, err = yaml.Marshal(pod); err != nil {
			msg := fmt.Sprintf("Failed marshaling pod yaml: %v", err)
			dlog.Error(ctx, msg)
			return msg, ""
		}
		return podLog, string(b)
	}
	return podLog, ""
}

// GatherLogs acquires the logs for the traffic-manager and/or traffic-agents specified by the
// connector.LogsRequest and returns them to the caller
func (tm *TrafficManager) GatherLogs(ctx context.Context, request *connector.LogsRequest) (*connector.LogsResponse, error) {
	logWriteMutex := &sync.Mutex{}
	coreAPI := k8sapi.GetK8sInterface(ctx).CoreV1()
	resp := &connector.LogsResponse{
		PodLogs: make(map[string]string),
		PodYaml: make(map[string]string),
	}
	container := "traffic-agent"
	hasContainer := func(pod *core.Pod) bool {
		if strings.EqualFold(request.Agents, "all") || strings.Contains(pod.Name, request.Agents) {
			cns := pod.Spec.Containers
			for c := range cns {
				if cns[c].Name == container {
					return true
				}
			}
		}
		return false
	}

	if !strings.EqualFold(request.Agents, "none") {
		for _, ns := range tm.GetCurrentNamespaces(true) {
			podsAPI := coreAPI.Pods(ns)
			podList, err := podsAPI.List(ctx, meta.ListOptions{})
			if err != nil {
				resp.ErrMsg = err.Error()
				dlog.Error(ctx, resp.ErrMsg)
				return resp, nil
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
					// Since the same named workload could exist in multiple namespaces
					// we add the namespace into the name so that it's easier to make
					// sense of the logs
					podAndNs := fmt.Sprintf("%s.%s", pod.Name, pod.Namespace)
					dlog.Debugf(ctx, "gathering logs for %s, yaml = %t", podAndNs, request.GetPodYaml)
					logs, yml := getPodLog(ctx, podsAPI, pod, container, request.GetPodYaml)
					logWriteMutex.Lock()
					resp.PodLogs[podAndNs] = logs
					if request.GetPodYaml {
						resp.PodYaml[podAndNs] = yml
					}
					logWriteMutex.Unlock()
				}(pod)
			}
			wg.Wait()
		}
	}

	// We want to get the traffic-manager log *last* so that if we generate
	// any errors in the traffic-manager getting the traffic-agent pods, we
	// want those logs to appear in what we export
	if request.TrafficManager {
		ns := tm.GetManagerNamespace()
		podAndNs := fmt.Sprintf("traffic-manager.%s", ns)
		podsAPI := coreAPI.Pods(ns)
		selector := labels.SelectorFromSet(labels.Set{
			"app":          "traffic-manager",
			"telepresence": "manager",
		})
		podList, err := podsAPI.List(ctx, meta.ListOptions{LabelSelector: selector.String()})
		switch {
		case err != nil:
			dlog.Errorf(ctx, "failed to gather logs for %s: %v", podAndNs, err)
			resp.PodLogs[podAndNs] = err.Error()
		case len(podList.Items) == 1:
			pod := &podList.Items[0]
			podAndNs = fmt.Sprintf("%s.%s", pod.Name, ns)
			dlog.Debugf(ctx, "gathering logs for %s, yaml = %t", podAndNs, request.GetPodYaml)
			logs, yml := getPodLog(ctx, podsAPI, pod, "traffic-manager", request.GetPodYaml)
			resp.PodLogs[podAndNs] = logs
			if request.GetPodYaml {
				resp.PodYaml[podAndNs] = yml
			}
		case len(podList.Items) > 1:
			msg := fmt.Sprintf("multiple traffic managers found in namespace %s using selector %s", ns, selector.String())
			dlog.Error(ctx, msg)
			resp.PodLogs[podAndNs] = msg
		default:
			msg := fmt.Sprintf("no traffic manager found in namespace %s using selector %s", ns, selector.String())
			dlog.Error(ctx, msg)
			resp.PodLogs[podAndNs] = msg
		}
	}
	return resp, nil
}
