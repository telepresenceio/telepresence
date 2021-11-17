package restapi_test

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/restapi"
)

type yesNo bool

func (yn yesNo) Intercepts(_ context.Context, _ string, _ http.Header) (bool, error) {
	return bool(yn), nil
}

type textMatcher map[string]string

func (t textMatcher) Intercepts(_ context.Context, _ string, header http.Header) (bool, error) {
	for k, v := range t {
		if header.Get(k) != v {
			return false, nil
		}
	}
	return true, nil
}

type callerIdMatcher string

func (c callerIdMatcher) Intercepts(_ context.Context, callerId string, _ http.Header) (bool, error) {
	return callerId == string(c), nil
}

func Test_server_intercepts(t *testing.T) {
	yes := yesNo(true)
	no := yesNo(false)

	tests := []struct {
		name    string
		agent   restapi.AgentState
		headers map[string]string
		client  bool
		want    bool
	}{
		{
			"true",
			yes,
			nil,
			true,
			true,
		},
		{
			"false",
			no,
			nil,
			true,
			false,
		},
		{
			"true",
			yes,
			nil,
			false,
			false,
		},
		{
			"false",
			no,
			nil,
			false,
			true,
		},
		{
			"header - match",
			textMatcher{
				restapi.HeaderInterceptID: "abc:123",
			},
			map[string]string{
				restapi.HeaderInterceptID: "abc:123",
			},
			true,
			true,
		},
		{
			"header - no match",
			textMatcher{
				restapi.HeaderInterceptID: "abc:123",
			},
			map[string]string{
				restapi.HeaderInterceptID: "xyz:123",
			},
			true,
			false,
		},
		{
			"header - match",
			textMatcher{
				restapi.HeaderInterceptID: "abc:123",
			},
			map[string]string{
				restapi.HeaderInterceptID: "abc:123",
			},
			false,
			false,
		},
		{
			"header - no match",
			textMatcher{
				restapi.HeaderInterceptID: "abc:123",
			},
			map[string]string{
				restapi.HeaderInterceptID: "xyz:123",
			},
			false,
			true,
		},
		{
			"multi header - all matched",
			textMatcher{
				"header-a": "value-a",
				"header-b": "value-b",
			},
			map[string]string{
				"header-a": "value-a",
				"header-b": "value-b",
				"header-c": "value-c",
			},
			true,
			true,
		},
		{
			"multi header - not all matched",
			textMatcher{
				"header-a": "value-a",
				"header-b": "value-b",
				"header-c": "value-c",
			},
			map[string]string{
				"header-a": "value-a",
				"header-b": "value-b",
			},
			true,
			false,
		},
		{
			"caller intercept id - match",
			callerIdMatcher("abc:123"),
			map[string]string{
				restapi.HeaderCallerInterceptID: "abc:123",
			},
			true,
			true,
		},
	}

	for _, tt := range tests {
		who := "agent"
		if tt.client {
			who = "client"
		}
		t.Run(fmt.Sprintf("%s: %s", who, tt.name), func(t *testing.T) {
			c, cancel := context.WithCancel(dlog.NewTestContext(t, false))
			ln, err := net.Listen("tcp", ":0")
			require.NoError(t, err)
			wg := sync.WaitGroup{}
			wg.Add(1)
			go func() {
				defer wg.Done()
				assert.NoError(t, restapi.NewServer(tt.agent, tt.client).Serve(c, ln))
			}()
			rq, err := http.NewRequest(http.MethodGet, "http://"+ln.Addr().String()+restapi.EndPontConsumeHere, nil)
			for k, v := range tt.headers {
				rq.Header.Set(k, v)
			}
			require.NoError(t, err)
			r, err := http.DefaultClient.Do(rq)
			require.NoError(t, err)
			defer r.Body.Close()
			assert.Equal(t, r.StatusCode, http.StatusOK)
			bt, err := io.ReadAll(r.Body)
			require.NoError(t, err)
			rpl, err := strconv.ParseBool(string(bt))
			assert.NoError(t, err)
			assert.Equal(t, tt.want, rpl)
			cancel()
			wg.Wait()
		})
	}
}
