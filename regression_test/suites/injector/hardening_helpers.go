package injector

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json/v2"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

const (
	injhardWait = 90 * time.Second
	injhardPoll = 2 * time.Second
)

// injhardApply writes manifest under the artifact directory and applies it
// with kubectl.
func injhardApply(ctx context.Context, r *rt.Runtime, ns, tag, manifest string) error {
	path := filepath.Join(r.ArtifactDir("manifests"), tag+".yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
		return err
	}
	_, err := r.Kubectl(ctx, ns, "apply", "-f", path)
	return err
}

// injhardManifestPath writes manifest under the artifact directory and returns its path.
func injhardManifestPath(r *rt.Runtime, tag, manifest string) (string, error) {
	path := filepath.Join(r.ArtifactDir("manifests"), tag+".yaml")
	return path, os.WriteFile(path, []byte(manifest), 0o644)
}

// injhardPod is a pod's UID and its sorted "container=image" entries.
type injhardPod struct {
	UID        string
	Containers []string
}

type injhardPodList struct {
	Items []struct {
		Metadata struct {
			UID               string            `json:"uid"`
			DeletionTimestamp string            `json:"deletionTimestamp"`
			Annotations       map[string]string `json:"annotations"`
		} `json:"metadata"`
		Spec struct {
			Containers []struct {
				Name  string `json:"name"`
				Image string `json:"image"`
			} `json:"containers"`
		} `json:"spec"`
	} `json:"items"`
}

// injhardPodsOf returns the non-terminating pods labeled app=name in ns.
func injhardPodsOf(ctx context.Context, r *rt.Runtime, ns, name string) ([]injhardPod, error) {
	var pl injhardPodList
	if err := r.KubectlJSON(ctx, ns, &pl, "get", "pod", "-l", "app="+name); err != nil {
		return nil, err
	}
	var pods []injhardPod
	for _, it := range pl.Items {
		if it.Metadata.DeletionTimestamp != "" {
			continue
		}
		p := injhardPod{UID: it.Metadata.UID}
		for _, c := range it.Spec.Containers {
			p.Containers = append(p.Containers, c.Name+"="+c.Image)
		}
		slices.Sort(p.Containers)
		pods = append(pods, p)
	}
	return pods, nil
}

// injhardAgentPodUIDs returns the UIDs of the non-terminating pods of name in
// ns that carry a traffic-agent container.
func injhardAgentPodUIDs(ctx context.Context, r *rt.Runtime, ns, name string) ([]string, error) {
	pods, err := injhardPodsOf(ctx, r, ns, name)
	if err != nil {
		return nil, err
	}
	var uids []string
	for _, p := range pods {
		for _, c := range p.Containers {
			if strings.HasPrefix(c, agentContainerName+"=") {
				uids = append(uids, p.UID)
				break
			}
		}
	}
	slices.Sort(uids)
	return uids, nil
}

// injhardRestoreManagerOnFailure restarts the traffic-manager at the end of
// the test when it failed, so that the shared release is usable afterwards.
func injhardRestoreManagerOnFailure(t *testing.T, env rt.Env) {
	t.Cleanup(func() {
		if !t.Failed() {
			return
		}
		if err := rt.RestartManager(env); err != nil {
			env.R.Infof("[rtest] injector: restarting the traffic-manager: %v", err)
		}
	})
}

// injhardManagerPod returns the name of the traffic-manager pod.
func injhardManagerPod(ctx context.Context, r *rt.Runtime) (string, error) {
	out, err := r.Kubectl(ctx, managers.ManagerNamespace, "get", "pod", "-l", "app=traffic-manager",
		"-o", "jsonpath={.items[0].metadata.name}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// injhardCallerManifest declares a ServiceAccount in ns that may only read
// pods and open port-forwards to them.
func injhardCallerManifest(ns, name string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: ServiceAccount
metadata:
  name: %[2]s
  namespace: %[1]s
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata:
  name: %[2]s
  namespace: %[1]s
rules:
  - apiGroups: [""]
    resources: ["pods"]
    verbs: ["get", "list"]
  - apiGroups: [""]
    resources: ["pods/portforward"]
    verbs: ["create"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata:
  name: %[2]s
  namespace: %[1]s
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: Role
  name: %[2]s
subjects:
  - kind: ServiceAccount
    name: %[2]s
    namespace: %[1]s
`, ns, name)
}

// injhardForward opens a port-forward to the traffic-manager pod's port
// remote as the ServiceAccount asSA in the manager namespace, and returns
// the local port. The forward ends with the test.
func injhardForward(t *testing.T, ctx context.Context, r *rt.Runtime, asSA, pod string, remote int) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()

	fctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = r.Kubectl(fctx, managers.ManagerNamespace, "--as", "system:serviceaccount:"+managers.ManagerNamespace+":"+asSA,
			"port-forward", "pod/"+pod, fmt.Sprintf("%d:%d", port, remote))
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-done:
			t.Fatalf("port-forward to %s:%d as %s ended early", pod, remote, asSA)
		default:
		}
		if c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second); err == nil {
			_ = c.Close()
			return port
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("port-forward to %s:%d as %s never became ready", pod, remote, asSA)
	return 0
}

// injhardWebhookDo sends one request to the webhook port behind a local
// forward and returns the status code and body.
func injhardWebhookDo(ctx context.Context, port int, method, path string, header map[string]string, body []byte) (int, []byte, error) {
	cl := &http.Client{
		Timeout: 20 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // the webhook's certificate is self-signed
			DisableKeepAlives: true,
		},
	}
	req, err := http.NewRequestWithContext(ctx, method, fmt.Sprintf("https://127.0.0.1:%d%s", port, path), bytes.NewReader(body))
	if err != nil {
		return 0, nil, err
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rsp, err := cl.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer rsp.Body.Close()
	b, err := io.ReadAll(rsp.Body)
	return rsp.StatusCode, b, err
}

// injhardJSON marshals v or fails the test.
func injhardJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshaling: %v", err)
	}
	return b
}
