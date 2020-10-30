package manager

import (
	"io/ioutil"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v2"

	"github.com/datawire/telepresence2/pkg/rpc"
)

func GetTestMechanisms(t *testing.T) map[string]*rpc.AgentInfo_Mechanism {
	data, err := ioutil.ReadFile(filepath.Join("testdata", "mechanisms.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	res := map[string]*rpc.AgentInfo_Mechanism{}

	if err := yaml.Unmarshal(data, res); err != nil {
		t.Fatal(err)
	}

	return res
}

func GetTestAgents(t *testing.T) map[string]*rpc.AgentInfo {
	data, err := ioutil.ReadFile(filepath.Join("testdata", "agents.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	res := map[string]*rpc.AgentInfo{}

	if err := yaml.Unmarshal(data, res); err != nil {
		t.Fatal(err)
	}

	return res
}

func GetTestClients(t *testing.T) map[string]*rpc.ClientInfo {
	data, err := ioutil.ReadFile(filepath.Join("testdata", "clients.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	res := map[string]*rpc.ClientInfo{}

	if err := yaml.Unmarshal(data, res); err != nil {
		t.Fatal(err)
	}

	return res
}
