package intercept

import (
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/telepresenceio/telepresence/v2/regression_test/framework/cli"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/managers"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/rt"
	"github.com/telepresenceio/telepresence/v2/regression_test/framework/workloads"
)

// envExcludedHost/envExcludedPassword are the workload's declared env vars
// that envExcludedSpec strips from an intercept's reported environment;
// envExcludedKeep is a third declared var that must survive untouched.
const (
	envExcludedHost     = "TP_TEST_DB_HOST"
	envExcludedPassword = "TP_TEST_DB_PASSWORD"
	envExcludedKeep     = "TP_TEST_KEEP"
)

// envExcludedSpec is a manager spec inline to this suite: intercept.
// environment.excluded (charts/telepresence-oss/values.schema.yaml), naming
// envExcludedHost and envExcludedPassword. The traffic-manager strips every
// listed name from an agent's/intercept's reported environment
// (cmd/traffic/cmd/manager/service.go's removeExcludedEnvVars, which deletes
// each configWatcher.GetAgentEnv().Excluded key; called from ArriveAsAgent
// for every container an agent session reports, and from ReviewIntercept for
// the environment an intercept reports once active). Built inline rather
// than added to the catalog since no other suite needs it.
//
//nolint:gochecknoglobals // registered once at init time, like every other manager spec
var envExcludedSpec = managers.Spec{
	Key: "env-excluded",
	Values: managers.Values{
		Intercept: managers.Intercept{
			Environment: managers.InterceptEnvironment{
				Excluded: []string{envExcludedHost, envExcludedPassword},
			},
		},
	},
}

// EnvExcluded proves that intercept.environment.excluded withholds the
// listed variable names from the environment an intercept hands to the
// client, while every other declared variable still reaches it.
type EnvExcluded struct {
	rt.Suite
}

func init() {
	rt.Register(&EnvExcluded{}, rt.InArea("intercept"), rt.NeedsManager(envExcludedSpec))
}

// envExcludedFileTimeout/envExcludedFilePollInterval bound the wait for the
// --env-file the intercept writes.
const (
	envExcludedFileTimeout      = 10 * time.Second
	envExcludedFilePollInterval = 250 * time.Millisecond
)

// Test_ExcludedVariablesFiltered attaches an intercept to a workload
// declaring both excluded names and a third, kept one, captures the
// intercept's environment to a file, and asserts the file eventually carries
// the kept variable but never either excluded name.
func (s *EnvExcluded) Test_ExcludedVariablesFiltered() {
	t := s.T()
	conn := s.Connect()

	tpl := workloads.Echo("env-excluded")
	tpl.Env = map[string]string{
		envExcludedHost:     "db",
		envExcludedPassword: "hunter2",
		envExcludedKeep:     "DATA",
	}
	wl := s.Workload(tpl)
	ls := s.LocalEcho()

	envFile := filepath.Join(t.TempDir(), "env.sh")
	a := conn.Intercept(t, wl, rt.ToLocal(ls, "http"), cli.MountFalse(), cli.EnvFile(envFile))
	defer a.Detach(t)

	var content string
	s.Eventually(func() bool {
		data, err := os.ReadFile(envFile)
		if err != nil {
			return false
		}
		content = string(data)
		return true
	}, envExcludedFileTimeout, envExcludedFilePollInterval, "env file %s never appeared", envFile)

	if !strings.Contains(content, envExcludedKeep+"=DATA") {
		t.Fatalf("env file %s: missing non-excluded %s\ncontent:\n%s", envFile, envExcludedKeep, content)
	}
	for _, excluded := range []string{envExcludedHost, envExcludedPassword} {
		if strings.Contains(content, excluded) {
			t.Fatalf("env file %s: excluded variable %s leaked into content\ncontent:\n%s", envFile, excluded, content)
		}
	}
}
