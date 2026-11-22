package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/rbacgen"
	"github.com/telepresenceio/telepresence/v2/pkg/errcat"
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

func TestReportVerdict(t *testing.T) {
	run := func(v verdict) (string, error) {
		cmd := &cobra.Command{}
		var out strings.Builder
		cmd.SetOut(&out)
		cmd.SetContext(context.Background())
		err := reportVerdict(cmd, v)
		return out.String(), err
	}

	out, err := run(&rbacgen.InstallVerdict{ManagerNamespace: "ambassador", Verbs: rbacgen.VerbsInstall})
	require.NoError(t, err)
	assert.Contains(t, out, "Nothing is missing")

	out, err = run(&rbacgen.InstallVerdict{
		ManagerNamespace: "ambassador", Verbs: rbacgen.VerbsInstall,
		Missing: []rbacgen.DeniedAttribute{{Verb: "create", Resource: "namespaces"}},
	})
	require.Error(t, err)
	assert.Equal(t, errcat.Silent, errcat.GetCategory(err), "the verdict already says what's missing")
	assert.Contains(t, out, "create namespaces")
}
