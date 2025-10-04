package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strconv"
	"sync"
	"time"

	core "k8s.io/api/core/v1"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/integration_test/itest"
	"github.com/telepresenceio/telepresence/v2/pkg/annotation"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
)

// request is the JSON structure used by the echo-server /forward endpoint.
type request struct {
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers,omitempty"`
	Method  string            `json:"method,omitempty"`
	Body    string            `json:"body,omitempty"`
}

type restAPISuite struct {
	itest.Suite
	itest.NamespacePair
	svc        string
	registry   string
	image      string
	apiPort    uint16
	daemonName string
}

func (s *restAPISuite) SuiteName() string {
	return "RESTful-API"
}

func init() {
	itest.AddNamespacePairSuite("", func(h itest.NamespacePair) itest.TestingSuite {
		return &restAPISuite{
			Suite:         itest.Suite{Harness: h},
			NamespacePair: h,
			svc:           "hello",
			apiPort:       9980,
			daemonName:    "alpha",
			registry:      "ghcr.io/telepresenceio",
			image:         "echo-server:latest",
		}
	})
}

func (s *restAPISuite) SetupSuite() {
	if !(s.ManagerIsVersion(">2.24.x") && s.ClientIsVersion(">2.24.x")) {
		s.T().Skip("Not part of compatibility suite")
	}
	if s.IsCI() && runtime.GOOS != "linux" {
		s.T().Skip("CI can't run linux docker containers inside non-linux runners")
		return
	}
	s.Suite.SetupSuite()
	ctx := s.Context()

	s.TelepresenceHelmInstallOK(ctx, false, "--set", fmt.Sprintf("telepresenceAPI.port=%d", s.apiPort))
	dep := &itest.Generic{
		Name:     s.svc,
		Registry: s.registry,
		Image:    s.image,
		Environment: []core.EnvVar{
			{
				Name:  "TELEPRESENCE_API_PORT",
				Value: strconv.Itoa(int(s.apiPort)),
			}, {
				Name:  "PORTS",
				Value: "8080,8081",
			},
		},
		Annotations: map[string]string{
			annotation.InjectTrafficAgent: "enabled",
		},
		ServicePorts: []itest.ServicePort{
			{
				Number:     80,
				Name:       "http",
				TargetPort: "http",
			},
			{
				Number:     81,
				Name:       "check",
				TargetPort: "check",
			},
		},
		ContainerPorts: []itest.ContainerPort{
			{
				Name:   "http",
				Number: 8080,
			},
			{
				Name:   "check",
				Number: 8081,
			},
		},
	}
	s.NoError(dep.Apply(ctx, s.AppNamespace()))
	s.CapturePodLogs(ctx, s.svc, "traffic-agent", s.AppNamespace())
	s.CapturePodLogs(ctx, s.svc, "", s.AppNamespace())
	s.TelepresenceConnect(ctx, "--docker", "--name", s.daemonName)
}

func (s *restAPISuite) TearDownSuite() {
	ctx := s.Context()
	itest.TelepresenceQuit(ctx)
	s.KubectlOk(ctx, "delete", "svc,deploy", s.svc)
	s.UninstallTrafficManager(ctx, s.ManagerNamespace())
}

func (s *restAPISuite) curlAPIServer(ctx context.Context, port uint16, path string, headers map[string]string, myArgs ...string) (string, error) {
	apiReq := &request{
		Headers: headers,
		URL:     fmt.Sprintf("http://${TELEPRESENCE_API_HOST}:${TELEPRESENCE_API_PORT}/%s?containerPort=%d", path, port),
	}
	apiReqJSON, err := json.Marshal(apiReq)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}
	args := []string{
		"curl",
		"--silent",
		"--max-time", "2",
		"-H", "Content-Type: application/json",
		"--request", "POST",
		"--data", string(apiReqJSON),
		fmt.Sprintf("http://%s/forward", s.svc),
	}
	args = append(args, myArgs...)
	so, se, err := itest.Telepresence(ctx, args...)
	if se != "" {
		dlog.Errorf(ctx, "stderr: %s", se)
	}
	return so, err
}

func (s *restAPISuite) startIntercept(ctx context.Context, meta, header string, errCh chan<- error, wg *sync.WaitGroup) {
	defer wg.Done()
	defer close(errCh)
	args := make([]string, 0, 15)
	args = append(args, "intercept", s.svc)
	if meta != "" {
		args = append(args, "--meta", meta)
	}
	if header != "" {
		args = append(args, "--http-header", header)
	}
	args = append(args, "--port", "8080:80", "--mount=false", "--docker-run", "--", "--rm", "--name", s.svc+"-local", s.registry+"/"+s.image)
	so, se, err := itest.Telepresence(ctx, args...)
	if so != "" {
		dlog.Infof(ctx, "stdout: %s", so)
	}
	if se != "" {
		dlog.Errorf(ctx, "stderr: %s", se)
	}
	if err != nil {
		errCh <- err
	}
}

func (s *restAPISuite) waitForInterceptReady(ctx context.Context, errCh <-chan error) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errCh:
			return err
		case <-ticker.C:
			st, err := itest.TelepresenceStatus(ctx)
			if err != nil {
				return err
			}
			for _, ic := range st.ContainerizedDaemon.Intercepts {
				if ic.Name == s.svc {
					return nil
				}
			}
		}
	}
}

func (s *restAPISuite) Test_RestAPI_GlobalConsume() {
	ctx := s.Context()
	rq := s.Require()

	so, err := s.curlAPIServer(ctx, 8080, "consume-here", nil)
	rq.NoError(err)
	dlog.Infof(ctx, "stdout: %s", so)
	var jv any
	err = json.Unmarshal([]byte(so), &jv)
	rq.NoError(err)
	yes, ok := jv.(bool)
	rq.True(ok)
	rq.True(yes)

	// When exiting, first cancel the intercept, then wait for it to exit.
	wg := &sync.WaitGroup{}
	iCtx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		wg.Wait()
	}()
	wg.Add(1)
	errCh := make(chan error, 1)
	go s.startIntercept(iCtx, "", "", errCh, wg)
	rq.NoError(s.waitForInterceptReady(iCtx, errCh))
	defer itest.TelepresenceOk(ctx, "leave", s.svc)

	so, err = s.curlAPIServer(ctx, 8080, "consume-here", map[string]string{restapi.HeaderCallerInterceptID: "${TELEPRESENCE_INTERCEPT_ID}"})
	rq.NoError(err)
	jv = nil
	err = json.Unmarshal([]byte(so), &jv)
	rq.NoError(err)
	dlog.Infof(ctx, "stdout: %s", so)

	// A global intercept will always hit the client, and the client's API server will always return true for the intercept id.
	yes, ok = jv.(bool)
	rq.True(ok)
	rq.True(yes)
}

func (s *restAPISuite) Test_RestAPI_GlobalInfo() {
	ctx := s.Context()
	rq := s.Require()

	so, err := s.curlAPIServer(ctx, 8080, "intercept-info", nil)
	rq.NoError(err)
	dlog.Infof(ctx, "stdout: %s", so)
	var jv any
	err = json.Unmarshal([]byte(so), &jv)
	rq.NoError(err)
	info, ok := jv.(map[string]any)
	rq.True(ok)
	yes, ok := info["intercepted"].(bool)
	rq.True(ok)
	s.False(yes)

	wg := &sync.WaitGroup{}
	iCtx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		wg.Wait()
	}()
	wg.Add(1)
	errCh := make(chan error, 1)
	go s.startIntercept(iCtx, "my:data", "", errCh, wg)
	rq.NoError(s.waitForInterceptReady(iCtx, errCh))
	defer itest.TelepresenceOk(ctx, "leave", s.svc)

	so, err = s.curlAPIServer(ctx, 8080, "intercept-info", map[string]string{restapi.HeaderCallerInterceptID: "${TELEPRESENCE_INTERCEPT_ID}"})
	rq.NoError(err)
	jv = nil
	err = json.Unmarshal([]byte(so), &jv)
	rq.NoError(err)
	dlog.Infof(ctx, "stdout: %s", so)
	info, ok = jv.(map[string]any)
	rq.True(ok)
	yes, ok = info["intercepted"].(bool)
	rq.True(ok)
	s.True(yes)

	data, ok := info["metadata"].(map[string]any)
	rq.True(ok)
	s.Equal("data", data["my"])
}

func (s *restAPISuite) Test_RestAPI_FilteredConsume() {
	ctx := s.Context()
	wg := &sync.WaitGroup{}
	iCtx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		wg.Wait()
	}()
	wg.Add(1)
	errCh := make(chan error, 1)
	go s.startIntercept(iCtx, "", "x:y", errCh, wg)
	s.Require().NoError(s.waitForInterceptReady(iCtx, errCh))
	defer itest.TelepresenceOk(ctx, "leave", s.svc)

	tts := []struct {
		name          string
		containerPort uint16
		apiHeaders    map[string]string
		args          []string
		expected      bool
	}{
		// Query the remote API server without any headers. It must respond true because this doesn't match any intercept.
		{
			name:          "query-remote-without-headers",
			containerPort: 8080,
			expected:      true,
		},
		// Query the remote API server with the intercepted header. The intercept is matched, so the remote API server
		// should tell the remote app to not consume the request.
		{
			name:          "query-remote-with-headers",
			containerPort: 8080,
			apiHeaders:    map[string]string{"x": "y"},
			expected:      false,
		},
		// Query the local API server without the intercepted header. The intercept is not matched, so the local API server
		// should tell the local app to not consume the request.
		{
			name:          "query-local-without-headers",
			containerPort: 8080,
			expected:      false,
			apiHeaders:    map[string]string{restapi.HeaderCallerInterceptID: "${TELEPRESENCE_INTERCEPT_ID}"},
			args:          []string{"-H", "x:y"},
		},
		// Query the local API server with the intercepted header. The intercept is matched, so the local API server
		// should tell the local app to consume the request.
		{
			name:          "query-local-with-headers",
			containerPort: 8080,
			apiHeaders:    map[string]string{restapi.HeaderCallerInterceptID: "${TELEPRESENCE_INTERCEPT_ID}", "x": "y"},
			args:          []string{"-H", "x:y"},
			expected:      true,
		},
	}
	for _, tt := range tts {
		s.Run(tt.name, func() {
			ctx := s.Context()
			rq := s.Require()
			so, err := s.curlAPIServer(ctx, tt.containerPort, "consume-here", tt.apiHeaders, tt.args...)
			rq.NoError(err)
			dlog.Infof(ctx, "stdout: %s", so)
			var jv any
			err = json.Unmarshal([]byte(so), &jv)
			rq.NoError(err)
			yes, ok := jv.(bool)
			rq.True(ok)
			s.Equal(tt.expected, yes)
		})
	}
}

func (s *restAPISuite) Test_RestAPI_FilteredInfo() {
	ctx := s.Context()
	wg := &sync.WaitGroup{}
	iCtx, cancel := context.WithCancel(ctx)
	defer func() {
		cancel()
		wg.Wait()
	}()
	wg.Add(1)
	errCh := make(chan error, 1)
	go s.startIntercept(iCtx, "my:data", "x:y", errCh, wg)
	s.Require().NoError(s.waitForInterceptReady(iCtx, errCh))
	defer itest.TelepresenceOk(ctx, "leave", s.svc)

	tts := []struct {
		name          string
		containerPort uint16
		apiHeaders    map[string]string
		args          []string
		intercepted   bool
		clientSide    bool
		myMeta        string
	}{
		// Query the remote API server without any headers. It must respond with client-side=false and intercepted=false because this doesn't match any intercept.'
		{
			name:          "query-remote-without-headers",
			containerPort: 8080,
			intercepted:   false,
			clientSide:    false,
			myMeta:        "",
		},
		// Query the remote API server with the intercepted header. It must respond with client-side=false and intercepted=true because the remote app is intercepted.
		{
			name:          "query-remote-with-headers",
			containerPort: 8080,
			apiHeaders:    map[string]string{"x": "y"},
			intercepted:   true,
			clientSide:    false,
			myMeta:        "data",
		},
		// Query the local API server without the intercepted header. The local API server must respond with client-side=true and intercepted=false.
		{
			name:          "query-local-without-headers",
			containerPort: 8080,
			apiHeaders:    map[string]string{restapi.HeaderCallerInterceptID: "${TELEPRESENCE_INTERCEPT_ID}"},
			intercepted:   false,
			clientSide:    true,
			args:          []string{"-H", "x:y"},
			myMeta:        "",
		},
		// Query the local API server with the intercepted header. The local API server must respond with client-side=true and intercepted=true
		{
			name:          "query-local-with-headers",
			containerPort: 8080,
			apiHeaders:    map[string]string{restapi.HeaderCallerInterceptID: "${TELEPRESENCE_INTERCEPT_ID}", "x": "y"},
			intercepted:   true,
			clientSide:    true,
			args:          []string{"-H", "x:y"},
			myMeta:        "data",
		},
	}
	for _, tt := range tts {
		s.Run(tt.name, func() {
			ctx := s.Context()
			rq := s.Require()
			so, err := s.curlAPIServer(ctx, tt.containerPort, "intercept-info", tt.apiHeaders, tt.args...)
			rq.NoError(err)
			dlog.Infof(ctx, "stdout: %s", so)
			var jv any
			err = json.Unmarshal([]byte(so), &jv)
			rq.NoError(err)
			dlog.Infof(ctx, "stdout: %s", so)
			info, ok := jv.(map[string]any)
			rq.True(ok)
			yes, ok := info["intercepted"].(bool)
			rq.True(ok)
			s.Equal(tt.intercepted, yes)

			// All intercepts have the same metadata.
			data, ok := info["metadata"].(map[string]any)
			if tt.myMeta != "" {
				rq.True(ok)
				s.Equal(tt.myMeta, data["my"])
			} else {
				s.False(ok)
			}
		})
	}
}
