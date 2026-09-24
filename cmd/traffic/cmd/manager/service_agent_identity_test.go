package manager

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/auth"
	"github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/managerutil"
)

func TestVerifiedAgentPrincipal(t *testing.T) {
	ctx := managerutil.WithEnv(context.Background(), &managerutil.Env{ManagerNamespace: "ambassador"})
	agent := func(nodeAgent bool) *rpc.AgentInfo {
		return &rpc.AgentInfo{Name: "echo", Namespace: "shop", PodName: "pod-1", PodUid: "uid-1", NodeAgent: nodeAgent}
	}
	principal := func(saNamespace string) *auth.Principal {
		return &auth.Principal{Username: "system:serviceaccount:" + saNamespace + ":default", PodName: "pod-1", PodUID: "uid-1"}
	}

	tests := []struct {
		name      string
		agent     *rpc.AgentInfo
		principal *auth.Principal
		bound     bool
		mismatch  bool
	}{
		{"no token", agent(false), nil, false, false},
		{"sidecar token from the workload namespace", agent(false), principal("shop"), true, false},
		{"sidecar token from the manager namespace", agent(false), principal("ambassador"), false, true},
		{"node-agent token from the manager namespace", agent(true), principal("ambassador"), true, false},
		{"node-agent token from the workload namespace", agent(true), principal("shop"), false, true},
		{"token for another pod", agent(false), &auth.Principal{Username: "system:serviceaccount:shop:default", PodName: "pod-2", PodUID: "uid-2"}, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := ctx
			if tt.principal != nil {
				c = auth.WithPrincipal(c, tt.principal)
			}
			p, mismatch := verifiedAgentPrincipal(c, tt.agent)
			assert.Equal(t, tt.mismatch, mismatch)
			assert.Equal(t, tt.bound, p != nil)
		})
	}
}
