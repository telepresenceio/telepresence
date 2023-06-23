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
