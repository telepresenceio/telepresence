package integration_test

import (
	"context"
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
// verify that the mount survives the scaling.
func (s *interceptMountSuite) Test_RestartInterceptedPod() {
	assert := s.Assert()
	require := s.Require()
	ctx := s.Context()
	rx := regexp.MustCompile(fmt.Sprintf(`Intercept name\s*: %s\s+State\s*: ([^\n]+)\n`, s.ServiceName()))

	// Scale down to zero pods
	require.NoError(s.Kubectl(ctx, "scale", "deploy", s.ServiceName(), "--replicas", "0"))

	// Wait until the pods have terminated. This might take a long time (several minutes).
	require.Eventually(func() bool { return len(s.runningPods(ctx)) == 0 }, 2*time.Minute, 6*time.Second)

	// Verify that intercept remains but that no agent is found. Use require here
	// to avoid a hanging os.Stat call unless this succeeds.
	require.Eventually(func() bool {
		stdout, _, err := itest.Telepresence(ctx, "list")
		if err != nil {
			return false
		}
		if match := rx.FindStringSubmatch(stdout); match != nil {
			return match[1] == "WAITING" || strings.Contains(match[1], `No agent found for "`+s.ServiceName()+`"`)
		}
		return false
	}, 30*time.Second, 3*time.Second)

	// Verify that volume mount is broken
	time.Sleep(time.Second) // avoid a stat just when the intercept became inactive as it sometimes causes a hang
	_, err := os.Stat(filepath.Join(s.mountPoint, "var", "run"))
	assert.Error(err, "Stat on <mount point>/var succeeded although no agent was found")

	// Scale up again (start intercepted pod)
	assert.NoError(s.Kubectl(ctx, "scale", "deploy", s.ServiceName(), "--replicas", "1"))
	assert.Eventually(func() bool { return len(s.runningPods(ctx)) == 1 }, itest.PodCreateTimeout(ctx), 6*time.Second)
	s.CapturePodLogs(ctx, s.ServiceName(), "traffic-agent", s.AppNamespace())

	// Verify that intercept becomes active
	assert.Eventually(func() bool {
		stdout, _, err := itest.Telepresence(ctx, "list")
		if err != nil {
			return false
		}
		if match := rx.FindStringSubmatch(stdout); match != nil {
			return match[1] == "ACTIVE"
		}
		return false
	}, 30*time.Second, 3*time.Second)

	// Verify that volume mount is restored
	time.Sleep(time.Second) // avoid a stat just when the intercept became active as it sometimes causes a hang
	assert.Eventually(func() bool {
		st, err := os.Stat(filepath.Join(s.mountPoint, "var", "run"))
		return err == nil && st.IsDir()
	}, 5*time.Second, time.Second)
}

// Test_StopInterceptedPodOfMany build belongs to the interceptMountSuite because we want to
// verify that the mount survives the scaling.
func (s *interceptMountSuite) Test_StopInterceptedPodOfMany() {
	assert := s.Assert()
	require := s.Require()
	ctx := s.Context()

	// Wait for exactly one active pod
	var currentPod string
	require.Eventually(func() bool {
		currentPods := s.runningPods(ctx)
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
		assert.Eventually(
			func() bool {
				return len(s.runningPods(ctx)) == 1
			}, 15*time.Second, time.Second)
		s.CapturePodLogs(ctx, s.ServiceName(), "traffic-agent", s.AppNamespace())
	}()

	// Wait for second pod to arrive
	assert.Eventually(func() bool { return len(s.runningPods(ctx)) == 2 }, itest.PodCreateTimeout(ctx), 6*time.Second)
	s.CapturePodLogs(ctx, s.ServiceName(), "traffic-agent", s.AppNamespace())

	// Delete the currently intercepted pod
	require.NoError(s.Kubectl(ctx, "delete", "pod", currentPod))

	// Wait for that pod to disappear and then be recreated
	assert.Eventually(
		func() bool {
			pods := s.runningPods(ctx)
			for _, zp := range pods {
				if zp == currentPod {
					return false
				}
			}
			return len(pods) == 2
		}, 15*time.Second, time.Second)
	s.CapturePodLogs(ctx, s.ServiceName(), "traffic-agent", s.AppNamespace())

	// Verify that intercept is still active
	rx := regexp.MustCompile(fmt.Sprintf(`Intercept name\s*: ` + s.ServiceName() + `\s+State\s*: ([^\n]+)\n`))
	assert.Eventually(func() bool {
		stdout, _, err := itest.Telepresence(ctx, "list", "--intercepts")
		if err != nil {
			return false
		}
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
	}, 30*time.Second, time.Second)

	// Verify that volume mount is restored
	time.Sleep(3 * time.Second) // avoid a stat just when the intercept became active as it sometimes causes a hang
	st, err := os.Stat(filepath.Join(s.mountPoint, "var"))
	require.NoError(err, "Stat on <mount point>/var failed")
	require.True(st.IsDir(), "<mount point>/var is not a directory")
}

// Return the names of running pods with app=<service name>. Running here means
// that at least one container is still running. I.e. the pod might well be terminating
// but still considered running.
func (s *interceptMountSuite) runningPods(ctx context.Context) []string {
	return itest.RunningPods(ctx, s.ServiceName(), s.AppNamespace())
}
