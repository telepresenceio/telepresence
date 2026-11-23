package intercept

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/check"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
)

const (
	dialpolSharedService = "dialpol-shared"
	dialpolSharedGroup   = "dialpol-shared-group"
	dialpolSharedImage   = "ghcr.io/telepresenceio/echo-server:0.3.1" // listens on 8080
	dialpolSharedPort    = 8080

	dialpolSharedWait   = 90 * time.Second
	dialpolSharedStreak = 10
)

// dialpolSharedManifest holds two single-replica Deployments whose pods
// carry the label "group", and a Service selecting that label, so the
// Service spans both Deployments. Arguments: namespace, group, service,
// image, port, then the two Deployment names.
const dialpolSharedManifest = `apiVersion: v1
kind: Service
metadata:
  name: %[3]s
  namespace: %[1]s
spec:
  selector:
    group: %[2]s
  ports:
    - name: http
      port: %[5]d
      targetPort: http
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: %[6]s
  namespace: %[1]s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: %[6]s
  template:
    metadata:
      labels:
        app: %[6]s
        group: %[2]s
    spec:
      containers:
        - name: echo
          image: %[4]s
          imagePullPolicy: IfNotPresent
          ports:
            - name: http
              containerPort: %[5]d
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: %[7]s
  namespace: %[1]s
spec:
  replicas: 1
  selector:
    matchLabels:
      app: %[7]s
  template:
    metadata:
      labels:
        app: %[7]s
        group: %[2]s
    spec:
      containers:
        - name: echo
          image: %[4]s
          imagePullPolicy: IfNotPresent
          ports:
            - name: http
              containerPort: %[5]d
`

// Test_SharedService creates a header-filtered intercept of one of two
// Deployments behind a common Service. The Service is then narrowed to each
// Deployment in turn, by taking the other one's pods out of its endpoints,
// and every request in each phase must reach the local listener.
func (s *dialpolTargets) Test_SharedService() {
	t := s.T()
	conn := s.Connect()
	ns := s.AppNamespace()
	r := s.R()
	ctx := s.Ctx()
	nameA, nameB := "dialpol-shared-a", "dialpol-shared-b"

	manifest := fmt.Sprintf(dialpolSharedManifest,
		ns, dialpolSharedGroup, dialpolSharedService, dialpolSharedImage, dialpolSharedPort, nameA, nameB)
	path := filepath.Join(t.TempDir(), "shared.yaml")
	if err := os.WriteFile(path, []byte(manifest), 0o644); err != nil {
		t.Fatalf("writing manifest: %v", err)
	}
	if _, err := r.Kubectl(ctx, ns, "apply", "-f", path); err != nil {
		t.Fatalf("applying shared-service workloads: %v", err)
	}
	t.Cleanup(func() {
		for _, args := range [][]string{
			{"delete", "deployment", nameA, nameB, "--ignore-not-found"},
			{"delete", "service", dialpolSharedService, "--ignore-not-found"},
		} {
			if _, err := r.Kubectl(ctx, ns, args...); err != nil {
				t.Errorf("cleanup %v: %v", args, err)
			}
		}
	})
	for _, name := range []string{nameA, nameB} {
		if _, err := r.Kubectl(ctx, ns, "rollout", "status", "deployment/"+name, "--timeout=120s"); err != nil {
			t.Fatalf("waiting for %s: %v", name, err)
		}
	}

	wl := &rt.Workload{
		Name:      nameA,
		Namespace: ns,
		Kind:      "Deployment",
		Port:      dialpolSharedPort,
		SvcName:   dialpolSharedService,
	}
	ls := s.LocalEcho()
	a := conn.Intercept(t, wl,
		rt.ToLocal(ls, "http"),
		cli.MountFalse(),
		cli.HTTPHeader(headerKey, headerVal),
	)
	defer a.Detach(t)

	url := wl.ServiceURL()
	rt.RoutedToLocal(t, url, ls, check.WithHeader(headerKey, headerVal))

	// The manager injects a traffic-agent into the second Deployment too.
	for _, name := range []string{nameA, nameB} {
		s.dialpolWaitAgent(ns, name)
	}

	for _, only := range []string{nameB, nameA} {
		other := nameA
		if only == nameA {
			other = nameB
		}
		s.dialpolSetGroup(ns, other, false)
		s.dialpolWaitEndpoints(ns, only, other)
		dialpolRequireStreak(s, url, ls)
		s.dialpolSetGroup(ns, other, true)
	}
}

// dialpolWaitAgent waits until every pod of the Deployment has a
// traffic-agent container and there is at least one.
func (s *dialpolTargets) dialpolWaitAgent(ns, name string) {
	t := s.T()
	deadline := time.Now().Add(dialpolSharedWait)
	for {
		out, err := s.R().Kubectl(s.Ctx(), ns, "get", "pods", "-l", "app="+name,
			"-o", `jsonpath={range .items[*]}{.spec.containers[*].name}{"\n"}{end}`)
		if trimmed := strings.TrimSpace(out); err == nil && trimmed != "" && dialpolAllHaveAgent(strings.Split(trimmed, "\n")) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pods of %s never all got a traffic-agent; last output %q, err %v", name, out, err)
		}
		time.Sleep(time.Second)
	}
}

func dialpolAllHaveAgent(lines []string) bool {
	for _, l := range lines {
		if !strings.Contains(l, "traffic-agent") {
			return false
		}
	}
	return true
}

// dialpolSetGroup adds or removes the Service-selected label on the pods of
// the Deployment.
func (s *dialpolTargets) dialpolSetGroup(ns, name string, member bool) {
	t := s.T()
	label := dialpolSharedGroup
	args := []string{"label", "pods", "-l", "app=" + name, "--overwrite"}
	if member {
		args = append(args, "group="+label)
	} else {
		args = append(args, "group-")
	}
	if _, err := s.R().Kubectl(s.Ctx(), ns, args...); err != nil {
		t.Fatalf("labeling pods of %s: %v", name, err)
	}
}

// dialpolWaitEndpoints waits until the Service's endpoints include a pod of
// only and none of other.
func (s *dialpolTargets) dialpolWaitEndpoints(ns, only, other string) {
	t := s.T()
	deadline := time.Now().Add(dialpolSharedWait)
	for {
		out, err := s.R().Kubectl(s.Ctx(), ns, "get", "endpoints", dialpolSharedService,
			"-o", "jsonpath={.subsets[*].addresses[*].targetRef.name}")
		if err == nil && strings.Contains(out, only+"-") && !strings.Contains(out, other+"-") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("endpoints of %s never narrowed to %s; last output %q, err %v", dialpolSharedService, only, out, err)
		}
		time.Sleep(time.Second)
	}
}

// dialpolRequireStreak fails unless dialpolSharedStreak consecutive requests
// with the filter header are all answered by ls, within dialpolSharedWait.
func dialpolRequireStreak(s *dialpolTargets, url string, ls *rt.LocalService) {
	t := s.T()
	t.Helper()
	client := &http.Client{
		Timeout:   2 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
	deadline := time.Now().Add(dialpolSharedWait)
	streak := 0
	var last string
	for streak < dialpolSharedStreak {
		if time.Now().After(deadline) {
			t.Fatalf("%s: only %d consecutive requests reached the local service; last: %s", url, streak, last)
		}
		ok, what := dialpolProbe(client, url, ls.Marker())
		if ok {
			streak++
			continue
		}
		streak, last = 0, what
		time.Sleep(250 * time.Millisecond)
	}
}

func dialpolProbe(client *http.Client, url, marker string) (bool, string) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return false, err.Error()
	}
	req.Header.Set(headerKey, headerVal)
	resp, err := client.Do(req)
	if err != nil {
		return false, err.Error()
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, err.Error()
	}
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), marker) {
		return false, fmt.Sprintf("status %d, body %q", resp.StatusCode, body)
	}
	return true, ""
}
