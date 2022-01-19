package integration_test

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/stretchr/testify/suite"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
)

type multipleServicesSuite struct {
	itest.Suite
	itest.MultipleServices
}

func init() {
	itest.AddMultipleServicesSuite("", "hello", func(h itest.MultipleServices) suite.TestingSuite {
		return &multipleServicesSuite{Suite: itest.Suite{Harness: h}, MultipleServices: h}
	})
}

func (s *multipleServicesSuite) Test_LargeRequest() {
	require := s.Require()
	client := &http.Client{Timeout: 3 * time.Minute}
	const sendSize = 1024 * 1024 * 5
	const concurrentRequests = 3

	wg := sync.WaitGroup{}
	wg.Add(concurrentRequests)
	for i := 0; i < concurrentRequests; i++ {
		go func() {
			defer wg.Done()
			b := make([]byte, sendSize)
			b[0] = '!'
			b[1] = '\n'
			for i := 2; i < sendSize; i++ {
				b[i] = 'A'
			}
			req, err := http.NewRequest(http.MethodPut, fmt.Sprintf("http://%s-0.%s/put", s.Name(), s.AppNamespace()), bytes.NewBuffer(b))
			require.NoError(err)

			resp, err := client.Do(req)
			require.NoError(err)
			defer resp.Body.Close()
			require.Equal(resp.StatusCode, 200)

			// Read start
			buf := make([]byte, 1)
			_, err = resp.Body.Read(buf)
			for err == nil && buf[0] != '!' {
				_, err = resp.Body.Read(buf)
			}
			require.NoError(err)
			_, err = resp.Body.Read(buf)
			require.Equal(buf[0], byte('\n'))
			require.NoError(err)

			buf = make([]byte, sendSize-2)
			i := 0
			for err == nil {
				var j int
				j, err = resp.Body.Read(buf[i:])
				i += j
			}

			require.Equal(len(buf), i)
			// Do this instead of require.Equal(b[2:], buf) so that on failure we don't print two 5MB buffers to the terminal
			require.Equal(true, bytes.Equal(b[2:], buf))
			require.Equal(io.EOF, err)
		}()
	}
	wg.Wait()
}

func (s *multipleServicesSuite) Test_List() {
	stdout := itest.TelepresenceOk(s.Context(), "-n", s.AppNamespace(), "list")
	for i := 0; i < s.ServiceCount(); i++ {
		s.Regexp(fmt.Sprintf(`%s-%d\s*: ready to intercept`, s.Name(), i), stdout)
	}
}

func (s *multipleServicesSuite) Test_ListOnlyMapped() {
	ctx := s.Context()
	require := s.Require()
	stdout := itest.TelepresenceOk(ctx, "connect", "--mapped-namespaces", "default")
	require.Empty(stdout)

	stdout = itest.TelepresenceOk(ctx, "list", "--namespace", s.AppNamespace())
	require.Contains(stdout, "No Workloads (Deployments, StatefulSets, or ReplicaSets)")

	stdout = itest.TelepresenceOk(ctx, "connect", "--mapped-namespaces", "all")
	require.Empty(stdout)

	stdout = itest.TelepresenceOk(ctx, "list", "--namespace", s.AppNamespace())
	require.NotContains(stdout, "No Workloads (Deployments, StatefulSets, or ReplicaSets)")
}

func (s *multipleServicesSuite) Test_ProxiesOutboundTraffic() {
	ctx := s.Context()
	for i := 0; i < s.ServiceCount(); i++ {
		svc := fmt.Sprintf("%s-%d.%s", s.Name(), i, s.AppNamespace())
		expectedOutput := fmt.Sprintf("Request served by %s-%d", s.Name(), i)
		s.Require().Eventually(
			// condition
			func() bool {
				dlog.Infof(ctx, "trying %q...", "http://"+svc)
				hc := http.Client{Timeout: time.Second}
				resp, err := hc.Get("http://" + svc)
				if err != nil {
					dlog.Error(ctx, err)
					return false
				}
				defer resp.Body.Close()
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					dlog.Error(ctx, err)
					return false
				}
				dlog.Infof(ctx, "body: %q", body)
				return strings.Contains(string(body), expectedOutput)
			},
			15*time.Second, // waitfor
			3*time.Second,  // polling interval
			`body of %q contains %q`, "http://"+svc, expectedOutput,
		)
	}
}
