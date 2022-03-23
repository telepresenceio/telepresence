package restapi_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
)

type yesNoClient bool
type yesNoCluster bool

func (yn yesNoClient) InterceptInfo(_ context.Context, _, _ string, _ uint16, _ http.Header) (*restapi.InterceptInfo, error) {
	return &restapi.InterceptInfo{Intercepted: bool(yn), ClientSide: true}, nil
}

func (yn yesNoCluster) InterceptInfo(_ context.Context, _, _ string, _ uint16, _ http.Header) (*restapi.InterceptInfo, error) {
	return &restapi.InterceptInfo{Intercepted: bool(yn), ClientSide: false}, nil
}

type textMatcher map[string]string
type textMatcherClient textMatcher
type textMatcherCluster textMatcher

func (t textMatcher) intercepted(header http.Header) bool {
	for k, v := range t {
		if header.Get(k) != v {
			return false
		}
	}
	return true
}

func (t textMatcherClient) InterceptInfo(_ context.Context, _, _ string, _ uint16, headers http.Header) (*restapi.InterceptInfo, error) {
	return &restapi.InterceptInfo{Intercepted: textMatcher(t).intercepted(headers), ClientSide: true}, nil
}

func (t textMatcherCluster) InterceptInfo(_ context.Context, _, _ string, _ uint16, headers http.Header) (*restapi.InterceptInfo, error) {
	return &restapi.InterceptInfo{Intercepted: textMatcher(t).intercepted(headers), ClientSide: false}, nil
}

type matcherWithMetadata struct {
	textMatcherCluster
	meta map[string]string
}

func (t *matcherWithMetadata) InterceptInfo(ctx context.Context, callerID, path string, containerPort uint16, headers http.Header) (*restapi.InterceptInfo, error) {
	ret, _ := t.textMatcherCluster.InterceptInfo(ctx, callerID, path, containerPort, headers)
	ret.Metadata = t.meta
	return ret, nil
}

type callerIdMatcherClient string

func (c callerIdMatcherClient) InterceptInfo(_ context.Context, callerID, _ string, _ uint16, _ http.Header) (*restapi.InterceptInfo, error) {
	return &restapi.InterceptInfo{Intercepted: callerID == string(c), ClientSide: true}, nil
}

type callerIdMatcherCluster string

func (c callerIdMatcherCluster) InterceptInfo(_ context.Context, callerID, _ string, _ uint16, _ http.Header) (*restapi.InterceptInfo, error) {
	return &restapi.InterceptInfo{Intercepted: callerID == string(c), ClientSide: false}, nil
}

func Test_server_intercepts(t *testing.T) {
	tests := []struct {
		name     string
		agent    restapi.AgentState
		headers  map[string]string
		endpoint string
		want     interface{}
	}{
		{
			"client true",
			yesNoClient(true),
			nil,
			restapi.EndPointConsumeHere,
			true,
		},
		{
			"client false",
			yesNoClient(false),
			nil,
			restapi.EndPointConsumeHere,
			false,
		},
		{
			"cluster true",
			yesNoCluster(true),
			nil,
			restapi.EndPointConsumeHere,
			false,
		},
		{
			"cluster false",
			yesNoCluster(false),
			nil,
			restapi.EndPointConsumeHere,
			true,
		},
		{
			"client header - match",
			textMatcherClient{
				restapi.HeaderInterceptID: "abc:123",
			},
			map[string]string{
				restapi.HeaderInterceptID: "abc:123",
			},
			restapi.EndPointConsumeHere,
			true,
		},
		{
			"client header - no match",
			textMatcherClient{
				restapi.HeaderInterceptID: "abc:123",
			},
			map[string]string{
				restapi.HeaderInterceptID: "xyz:123",
			},
			restapi.EndPointConsumeHere,
			false,
		},
		{
			"cluster header - match",
			textMatcherCluster{
				restapi.HeaderInterceptID: "abc:123",
			},
			map[string]string{
				restapi.HeaderInterceptID: "abc:123",
			},
			restapi.EndPointConsumeHere,
			false,
		},
		{
			"cluster header - match",
			&matcherWithMetadata{
				textMatcherCluster: textMatcherCluster{
					restapi.HeaderInterceptID: "abc:123",
				},
				meta: map[string]string{
					"a": "A",
				},
			},
			map[string]string{
				restapi.HeaderInterceptID: "abc:123",
			},
			restapi.EndPointInterceptInfo,
			&restapi.InterceptInfo{
				Intercepted: true,
				ClientSide:  false,
				Metadata: map[string]string{
					"a": "A",
				},
			},
		},
		{
			"cluster header - no match",
			textMatcherCluster{
				restapi.HeaderInterceptID: "abc:123",
			},
			map[string]string{
				restapi.HeaderInterceptID: "xyz:123",
			},
			restapi.EndPointConsumeHere,
			true,
		},
		{
			"client multi header - all matched",
			textMatcherClient{
				"header-a": "value-a",
				"header-b": "value-b",
			},
			map[string]string{
				"header-a": "value-a",
				"header-b": "value-b",
				"header-c": "value-c",
			},
			restapi.EndPointConsumeHere,
			true,
		},
		{
			"client multi header - not all matched",
			textMatcherClient{
				"header-a": "value-a",
				"header-b": "value-b",
				"header-c": "value-c",
			},
			map[string]string{
				"header-a": "value-a",
				"header-b": "value-b",
			},
			restapi.EndPointConsumeHere,
			false,
		},
		{
			"client caller intercept id - match",
			callerIdMatcherClient("abc:123"),
			map[string]string{
				restapi.HeaderCallerInterceptID: "abc:123",
			},
			restapi.EndPointConsumeHere,
			true,
		},
		{
			"client caller intercept id - match",
			callerIdMatcherClient("abc:123"),
			map[string]string{
				restapi.HeaderCallerInterceptID: "abc:123",
			},
			restapi.EndPointInterceptInfo,
			&restapi.InterceptInfo{
				Intercepted: true,
				ClientSide:  true,
				Metadata:    nil,
			},
		},
		{
			"cluster caller intercept id - match",
			callerIdMatcherCluster("abc:123"),
			map[string]string{
				restapi.HeaderCallerInterceptID: "abc:123",
			},
			restapi.EndPointInterceptInfo,
			&restapi.InterceptInfo{
				Intercepted: true,
				ClientSide:  false,
				Metadata:    nil,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, cancel := context.WithCancel(dlog.NewTestContext(t, false))
			ln, err := net.Listen("tcp", ":0")
			require.NoError(t, err)
			wg := sync.WaitGroup{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				assert.NoError(t, restapi.NewServer(tt.agent).Serve(c, ln))
			}()
			rq, err := http.NewRequest(http.MethodGet, "http://"+ln.Addr().String()+tt.endpoint, nil)
			for k, v := range tt.headers {
				rq.Header.Set(k, v)
			}
			require.NoError(t, err)
			r, err := http.DefaultClient.Do(rq)
			require.NoError(t, err)
			defer r.Body.Close()
			assert.Equal(t, r.StatusCode, http.StatusOK)
			if _, ok := tt.want.(bool); ok {
				var rpl bool
				require.NoError(t, json.NewDecoder(r.Body).Decode(&rpl))
				assert.Equal(t, tt.want, rpl)
			} else {
				var rpl restapi.InterceptInfo
				require.NoError(t, json.NewDecoder(r.Body).Decode(&rpl))
				assert.Equal(t, tt.want, &rpl)
			}
			cancel()
			wg.Wait()
		})
	}
}
