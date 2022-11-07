package test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	rpc "github.com/telepresenceio/telepresence/rpc/v2/manager"
)

func callerPackage(skip int) string {
	pc, _, _, _ := runtime.Caller(skip) //nolint:dogsled // stdlib, can't change it
	name := runtime.FuncForPC(pc).Name()
	// name is "foo.bar/baz/pkg.func1.func2"; we want
	// "foo.bar/baz/pkg".  That is: We trim at the first dot after
	// the last slash.  This logic is similar to that from
	// github.com/pkg/errors.funcname().
	slash := strings.LastIndex(name, "/")
	dot := slash + strings.Index(name[slash:], ".")
	return name[:dot]
}

var thispackage = callerPackage(0) //nolint:gochecknoglobals // unit tests only

func GetTestMechanisms(t *testing.T) map[string]*rpc.AgentInfo_Mechanism {
	basedir, err := filepath.Rel(callerPackage(2), thispackage)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(basedir, "testdata", "mechanisms.yaml"))
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
	basedir, err := filepath.Rel(callerPackage(2), thispackage)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(basedir, "testdata", "agents.yaml"))
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
	basedir, err := filepath.Rel(callerPackage(2), thispackage)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(basedir, "testdata", "clients.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	res := map[string]*rpc.ClientInfo{}

	if err := yaml.Unmarshal(data, res); err != nil {
		t.Fatal(err)
	}

	return res
}
