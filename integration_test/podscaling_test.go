package integration_test

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

// Test_RestartInterceptedPod build belongs to the interceptMountSuite because we want to
// verify that the mount survives the scaling
func (s *interceptMountSuite) Test_RestartInterceptedPod() {
	assert := s.Assert()
	require := s.Require()
	ctx := s.Context()
	rx := regexp.MustCompile(fmt.Sprintf(`Intercept name\s*: %s-%s\s+State\s*: ([^\n]+)\n`, s.ServiceName(), s.AppNamespace()))

	// Scale down to zero pods
	require.NoError(s.Kubectl(ctx, "scale", "deploy", s.ServiceName(), "--replicas", "0"))

	// Verify that intercept remains but that no agent is found. User require here
	// to avoid a hanging os.Stat call unless this succeeds.
	require.Eventually(func() bool {
		stdout := itest.TelepresenceOk(ctx, "--namespace", s.AppNamespace(), "list")
		if match := rx.FindStringSubmatch(stdout); match != nil {
			return match[1] == "WAITING" || strings.Contains(match[1], `No agent found for "`+s.ServiceName()+`"`)
		}
		return false
	}, 15*time.Second, time.Second)

	// Verify that volume mount is broken
	time.Sleep(time.Second) // avoid a stat just when the intercept became inactive as it sometimes causes a hang
	_, err := os.Stat(filepath.Join(s.mountPoint, "var"))
	assert.Error(err, "Stat on <mount point>/var succeeded although no agent was found")

	// Scale up again (start intercepted pod)
	assert.NoError(s.Kubectl(ctx, "scale", "deploy", s.ServiceName(), "--replicas", "1"))

	// Verify that intercept becomes active
	require.Eventually(func() bool {
		stdout := itest.TelepresenceOk(ctx, "--namespace", s.AppNamespace(), "list")
		if match := rx.FindStringSubmatch(stdout); match != nil {
			return match[1] == "ACTIVE"
		}
		return false
	}, 15*time.Second, time.Second)

	// Verify that volume mount is restored
	time.Sleep(time.Second) // avoid a stat just when the intercept became active as it sometimes causes a hang
	assert.Eventually(func() bool {
		st, err := os.Stat(filepath.Join(s.mountPoint, "var"))
		return err == nil && st.IsDir()
	}, 5*time.Second, time.Second)
}

// Test_StopInterceptedPodOfMany build belongs to the interceptMountSuite because we want to
// verify that the mount survives the scaling
func (s *interceptMountSuite) Test_StopInterceptedPodOfMany() {
	assert := s.Assert()
	require := s.Require()
	ctx := s.Context()

	// Terminating is not a state, so you may want to wrap calls to this function in an eventually
	// to give any pods that are terminating the chance to complete.
	// https://kubernetes.io/docs/concepts/workloads/pods/pod-lifecycle/
	helloPods := func() []string {
		pods, err := s.KubectlOut(ctx, "get", "pods",
			"--field-selector", "status.phase==Running",
			"-l", "app="+s.ServiceName(),
			"-o", "jsonpath={.items[*].metadata.name}")
		assert.NoError(err)
		pods = strings.TrimSpace(pods)
		dlog.Infof(ctx, "Pods = '%s'", pods)
		return strings.Split(pods, " ")
	}

	// Wait for exactly one active pod
	var currentPod string
	require.Eventually(func() bool {
		currentPods := helloPods()
		if len(currentPods) == 1 {
			currentPod = currentPods[0]
			return true
		}
		return false
	}, 20*time.Second, 2*time.Second)

	// Scale up to two pods
	require.NoError(s.Kubectl(ctx, "scale", "deploy", s.ServiceName(), "--replicas", "2"))
	defer func() {
		_ = s.Kubectl(ctx, "scale", "deploy", s.ServiceName(), "--replicas", "1")
	}()

	// Wait for second pod to arrive
	assert.Eventually(func() bool { return len(helloPods()) == 2 }, 5*time.Second, time.Second)

	// Delete the currently intercepted pod
	require.NoError(s.Kubectl(ctx, "delete", "pod", currentPod))

	// Wait for that pod to disappear
	assert.Eventually(
		func() bool {
			for _, zp := range helloPods() {
				if zp == currentPod {
					return false
				}
			}
			return true
		}, 15*time.Second, time.Second)

	// Verify that intercept is still active
	rx := regexp.MustCompile(fmt.Sprintf(`Intercept name\s*: ` + s.ServiceName() + `-` + s.AppNamespace() + `\s+State\s*: ([^\n]+)\n`))
	assert.Eventually(func() bool {
		stdout := itest.TelepresenceOk(ctx, "--namespace", s.AppNamespace(), "list", "--intercepts")
		dlog.Debugf(ctx, "match %q in %q", rx.String(), stdout)
		if match := rx.FindStringSubmatch(stdout); match != nil {
			return match[1] == "ACTIVE"
		}
		return false
	}, 15*time.Second, time.Second)

	// Verify response from intercepting client
	require.Eventually(func() bool {
		hc := http.Client{Timeout: time.Second}
		resp, err := hc.Get("http://" + s.ServiceName())
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return false
		}
		return s.ServiceName()+" from intercept at /" == string(body)
	}, 15*time.Second, time.Second)

	// Verify that volume mount is restored
	time.Sleep(time.Second) // avoid a stat just when the intercept became active as it sometimes causes a hang
	st, err := os.Stat(filepath.Join(s.mountPoint, "var"))
	require.NoError(err, "Stat on <mount point>/var failed")
	require.True(st.IsDir(), "<mount point>/var is not a directory")
}
