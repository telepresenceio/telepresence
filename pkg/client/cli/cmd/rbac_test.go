package cmd

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
)

func TestCallerRequest(t *testing.T) {
	cr := &connector.ConnectRequest{KubeFlags: map[string]string{
		"as":            "alice",
		"as-group":      "devs",
		"as-uid":        "1",
		"as-user-extra": "k=v",
		"context":       "dev",
	}}
	caller := callerRequest(cr)
	assert.Equal(t, map[string]string{"context": "dev"}, caller.KubeFlags)
	assert.Equal(t, "alice", cr.KubeFlags["as"], "the request with --as is left as is")
}

func TestRbacVerifyFlags(t *testing.T) {
	cmd := rbacCmd()
	install, _, err := cmd.Find([]string{"verify", "install"})
	assert.NoError(t, err)
	assert.NotNil(t, install.Flags().Lookup("verbs"))
	assert.NotNil(t, install.Flags().Lookup("input"))
	client, _, err := cmd.Find([]string{"verify", "client"})
	assert.NoError(t, err)
	for _, f := range []string{"namespace", "workload", "input", "as"} {
		assert.NotNil(t, client.Flags().Lookup(f), f)
	}
}
