package manager_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/datawire/telepresence2/cmd/traffic/cmd/manager"
)

func TestEnvconfig(t *testing.T) {
	origEnv := os.Environ()
	defer func() {
		os.Clearenv()
		for _, kv := range origEnv {
			eq := strings.Index(kv, "=")
			if eq < 0 {
				continue
			}
			k := kv[:eq]
			v := kv[eq+1:]
			os.Setenv(k, v)
		}
	}()

	defaults := manager.Env{
		ClusterEnv: manager.ClusterEnv{
			AmbassadorClusterID:       "07eb43c8-1166-5145-a060-45e4dd907e10",
			AmbassadorSingleNamespace: false,
			AmbassadorNamespace:       "default",
			AmbassadorID:              "default",
		},
		User:        "",
		ServerHost:  "",
		ServerPort:  "8081",
		SystemAHost: "beta-app.datawire.io",
		SystemAPort: "443",
	}

	testcases := map[string]struct {
		Input  map[string]string
		Output func(*manager.Env)
	}{
		"empty": {
			Input:  nil,
			Output: func(*manager.Env) {},
		},
		"scout-id": {
			Input: map[string]string{
				"AMBASSADOR_SCOUT_ID": "foo",
			},
			Output: func(e *manager.Env) {
				e.AmbassadorClusterID = "foo"
			},
		},
		"cluster-id": {
			Input: map[string]string{
				"AMBASSADOR_CLUSTER_ID": "bar",
			},
			Output: func(e *manager.Env) {
				e.AmbassadorClusterID = "bar"
			},
		},
		"scout-and-clustercluster-id": {
			Input: map[string]string{
				"AMBASSADOR_SCOUT_ID":   "foo",
				"AMBASSADOR_CLUSTER_ID": "bar",
			},
			Output: func(e *manager.Env) {
				e.AmbassadorClusterID = "bar"
			},
		},
		"single-ns-0": {
			Input: map[string]string{},
			Output: func(e *manager.Env) {
				e.AmbassadorSingleNamespace = false
			},
		},
		"single-ns-1": {
			Input: map[string]string{
				"AMBASSADOR_SINGLE_NAMESPACE": "",
			},
			Output: func(e *manager.Env) {
				e.AmbassadorSingleNamespace = false
			},
		},
		"single-ns-2": {
			Input: map[string]string{
				"AMBASSADOR_SINGLE_NAMESPACE": "true",
			},
			Output: func(e *manager.Env) {
				e.AmbassadorSingleNamespace = true
			},
		},
		"single-ns-3": {
			// Check that it's an empty/nonempty check, not strconv.ParseBool
			Input: map[string]string{
				"AMBASSADOR_SINGLE_NAMESPACE": "false",
			},
			Output: func(e *manager.Env) {
				e.AmbassadorSingleNamespace = true
			},
		},
	}

	for tcName, tc := range testcases {
		tc := tc // Capture loop variable...
		// Because we don't run the subtests in parallel, capturing the loop variable
		// doesn't really matter, but scopelint complains.

		t.Run(tcName, func(t *testing.T) {
			assert := assert.New(t)

			os.Clearenv()
			for k, v := range tc.Input {
				os.Setenv(k, v)
			}

			expected := defaults
			tc.Output(&expected)

			actual, err := manager.LoadEnv(context.Background())
			assert.Nil(err)
			assert.Equal(expected, actual)
		})
	}
}
