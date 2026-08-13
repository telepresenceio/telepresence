package managerutil

import (
	"testing"

	"github.com/stretchr/testify/assert"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
	testdata "github.com/telepresenceio/telepresence/v2/cmd/traffic/cmd/manager/test"
)

func TestMechanismsAreTheSame(t *testing.T) {
	a := assert.New(t)

	testMechs := testdata.GetTestMechanisms(t)
	testAgents := testdata.GetTestAgents(t)

	empty := []*rpc.AgentInfo_Mechanism{}
	oss := testAgents["hello"].Mechanisms
	plus := testAgents["helloPro"].Mechanisms
	sameAsPlus := []*rpc.AgentInfo_Mechanism{testMechs["http"], testMechs["grpc"], testMechs["tcp"]}
	plus2 := []*rpc.AgentInfo_Mechanism{testMechs["tcp"], testMechs["grpc"], testMechs["httpv2"]}
	bogus := []*rpc.AgentInfo_Mechanism{testMechs["tcp"], testMechs["http"], testMechs["httpv2"]} // 2 http

	a.False(mechanismsAreTheSame(empty, empty))
	a.False(mechanismsAreTheSame(oss, plus))
	a.False(mechanismsAreTheSame(plus, plus2))
	a.False(mechanismsAreTheSame(plus, bogus))
	a.True(mechanismsAreTheSame(plus, sameAsPlus))
	a.True(mechanismsAreTheSame(testAgents["demo1"].Mechanisms, testAgents["demo2"].Mechanisms))
	a.True(mechanismsAreTheSame(oss, []*rpc.AgentInfo_Mechanism{testMechs["tcp"]}))
}

func TestAgentsAreCompatible(t *testing.T) {
	a := assert.New(t)

	testAgents := testdata.GetTestAgents(t)
	helloAgent := testAgents["hello"]
	helloProAgent := testAgents["helloPro"]
	demoAgent1 := testAgents["demo1"]
	demoAgent2 := testAgents["demo2"]

	a.True(AgentsAreCompatible([]*rpc.AgentInfo{demoAgent1, demoAgent2}))
	a.True(AgentsAreCompatible([]*rpc.AgentInfo{helloAgent}))
	a.True(AgentsAreCompatible([]*rpc.AgentInfo{helloProAgent}))
	a.False(AgentsAreCompatible([]*rpc.AgentInfo{}))
	a.False(AgentsAreCompatible([]*rpc.AgentInfo{helloAgent, helloProAgent}))
}

func TestVersionsAreCompatible(t *testing.T) {
	for _, tc := range []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{name: "identical release", a: "2.31.2", b: "2.31.2", want: true},
		{name: "different prerelease builds", a: "2.31.2-custom.18", b: "2.31.2-custom.20", want: true},
		{name: "different build metadata", a: "2.31.2+build.18", b: "2.31.2+build.20", want: true},
		{name: "release and prerelease", a: "2.31.2", b: "2.31.2-rc.1", want: true},
		{name: "optional v prefix", a: "v2.31.2-rc.1", b: "2.31.2-rc.2", want: true},
		{name: "different major", a: "2.31.2-rc.1", b: "3.31.2-rc.1", want: false},
		{name: "different minor", a: "2.31.2-rc.1", b: "2.32.2-rc.1", want: false},
		{name: "different patch", a: "2.31.2-rc.1", b: "2.31.3-rc.1", want: false},
		{name: "same malformed version", a: "development", b: "development", want: true},
		{name: "different malformed versions", a: "development", b: "development-next", want: false},
		{name: "malformed first version", a: "development", b: "2.31.2", want: false},
		{name: "malformed second version", a: "2.31.2", b: "development", want: false},
		{name: "empty versions remain equal", a: "", b: "", want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, versionsAreCompatible(tc.a, tc.b))
			assert.Equal(t, tc.want, versionsAreCompatible(tc.b, tc.a))
		})
	}
}

func TestAgentsAreCompatibleDuringSameReleaseRollout(t *testing.T) {
	newAgent := func(version, mechanismVersion string) *rpc.AgentInfo {
		return &rpc.AgentInfo{
			Name:    "example-workload",
			Product: "telepresence",
			Version: version,
			Mechanisms: []*rpc.AgentInfo_Mechanism{{
				Name:    "http",
				Product: "telepresence",
				Version: mechanismVersion,
			}},
		}
	}

	for _, tc := range []struct {
		name  string
		first *rpc.AgentInfo
		next  *rpc.AgentInfo
		want  bool
	}{
		{
			name:  "same release with different agent and mechanism builds",
			first: newAgent("2.31.2-custom.18", "2.31.2-custom.18"),
			next:  newAgent("2.31.2-custom.20", "2.31.2-custom.20"),
			want:  true,
		},
		{
			name:  "different agent patch",
			first: newAgent("2.31.2-custom.18", "2.31.2-custom.18"),
			next:  newAgent("2.31.3-custom.20", "2.31.2-custom.20"),
		},
		{
			name:  "different mechanism patch",
			first: newAgent("2.31.2-custom.18", "2.31.2-custom.18"),
			next:  newAgent("2.31.2-custom.20", "2.31.3-custom.20"),
		},
		{
			name:  "different product",
			first: newAgent("2.31.2-custom.18", "2.31.2-custom.18"),
			next: func() *rpc.AgentInfo {
				agent := newAgent("2.31.2-custom.20", "2.31.2-custom.20")
				agent.Product = "different-product"
				return agent
			}(),
		},
		{
			name:  "different workload",
			first: newAgent("2.31.2-custom.18", "2.31.2-custom.18"),
			next: func() *rpc.AgentInfo {
				agent := newAgent("2.31.2-custom.20", "2.31.2-custom.20")
				agent.Name = "different-workload"
				return agent
			}(),
		},
		{
			name:  "different mechanism product",
			first: newAgent("2.31.2-custom.18", "2.31.2-custom.18"),
			next: func() *rpc.AgentInfo {
				agent := newAgent("2.31.2-custom.20", "2.31.2-custom.20")
				agent.Mechanisms[0].Product = "different-product"
				return agent
			}(),
		},
		{
			name:  "different mechanism name",
			first: newAgent("2.31.2-custom.18", "2.31.2-custom.18"),
			next: func() *rpc.AgentInfo {
				agent := newAgent("2.31.2-custom.20", "2.31.2-custom.20")
				agent.Mechanisms[0].Name = "tcp"
				return agent
			}(),
		},
		{
			name:  "different malformed agent versions",
			first: newAgent("development", "2.31.2-custom.18"),
			next:  newAgent("development-next", "2.31.2-custom.20"),
		},
		{
			name:  "identical malformed versions",
			first: newAgent("development", "development"),
			next:  newAgent("development", "development"),
			want:  true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, AgentsAreCompatible([]*rpc.AgentInfo{tc.first, tc.next}))
			assert.Equal(t, tc.want, AgentsAreCompatible([]*rpc.AgentInfo{tc.next, tc.first}))
		})
	}
}
