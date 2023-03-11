package cmd

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_legacyCommands(t *testing.T) {
	type testcase struct {
		name               string
		inputLegacyCommand string
		outputTP2Command   string
		msg                string
	}
	testCases := []testcase{
		{
			name:               "swapDeploymentBasic",
			inputLegacyCommand: "telepresence --swap-deployment myserver --expose 9090 --run python3 -m http.server 9090",
			outputTP2Command:   "intercept myserver --port 9090 -- python3 -m http.server 9090",
		},
		{
			name:               "swapDeploymentMethod",
			inputLegacyCommand: "telepresence --swap-deployment myserver --method inject-tcp --expose 9090 --run python3 -m http.server 9090",
			outputTP2Command:   "intercept myserver --port 9090 -- python3 -m http.server 9090",
			msg:                "Telepresence doesn't have proxying methods. You can use --docker-run for container, otherwise it works similarly to vpn-tcp\n",
		},
		{
			name:               "swapDeploymentUnsupportedParam",
			inputLegacyCommand: "telepresence --swap-deployment myserver --expose 9090 --not-real-param --run python3 -m http.server 9090",
			outputTP2Command:   "intercept myserver --port 9090 -- python3 -m http.server 9090",
			msg:                "The following flags used don't have a direct translation to Telepresence: --not-real-param\n",
		},
		{
			// This name isn't the greatest but basically, if we have an unsupported
			// parameter in the process, that's fine because telepresence doesn't
			// care about parameters associated with a process a user is running.
			name:               "swapDeploymentUnsupportedParamInProcess",
			inputLegacyCommand: "telepresence --swap-deployment myserver --expose 9090 --run python3 -m http.server 9090 --not-real-param",
			outputTP2Command:   "intercept myserver --port 9090 -- python3 -m http.server 9090 --not-real-param",
		},
		{
			name:               "swapDeploymentMappedPort",
			inputLegacyCommand: "telepresence --swap-deployment myserver --expose 9090:80 --run python3 -m http.server 9090",
			outputTP2Command:   "intercept myserver --port 9090:80 -- python3 -m http.server 9090",
		},
		{
			name:               "swapDeploymentRunShell",
			inputLegacyCommand: "telepresence --swap-deployment myserver --run-shell",
			outputTP2Command:   "intercept myserver -- bash",
		},
		{
			name:               "swapDeploymentBasicDockerRun",
			inputLegacyCommand: "telepresence --swap-deployment myserver --expose 80 --docker-run -i -t nginx:latest",
			outputTP2Command:   "intercept myserver --port 80 --docker-run -- -i -t nginx:latest",
		},
		{
			name:               "swapDeploymentGlobalFlag",
			inputLegacyCommand: "telepresence --as system:serviceaccount:default:telepresence-test-developer --swap-deployment myserver --expose 9090 --run python3 -m http.server 9090",
			outputTP2Command:   "intercept myserver --port 9090 --as system:serviceaccount:default:telepresence-test-developer -- python3 -m http.server 9090",
		},
		{
			name:               "runCommand",
			inputLegacyCommand: "telepresence --run curl http://myservice:8080/",
			outputTP2Command:   "connect -- curl http://myservice:8080/",
		},
		{
			name:               "runShell",
			inputLegacyCommand: "telepresence --run-shell",
			outputTP2Command:   "connect -- bash",
		},
		{
			name:               "runShellNewDeployment",
			inputLegacyCommand: "telepresence --new-deployment myserver --run-shell",
			outputTP2Command:   "connect -- bash",
			msg:                "This flag is ignored since Telepresence uses one traffic-manager deployed in the ambassador namespace.\n",
		},
		{
			name:               "runShellIgnoreExtraArgs",
			inputLegacyCommand: "telepresence --expose 8080 --run-shell",
			outputTP2Command:   "connect -- bash",
		},
	}

	for _, tc := range testCases {
		tcName := tc.name
		tc := tc
		t.Run(tcName, func(t *testing.T) {
			inputArgs := strings.Split(tc.inputLegacyCommand, " ")
			genTPCmd, msg, _, err := translateLegacy(inputArgs)
			if err != nil {
				t.Fatal(err)
			}
			assert.Equal(t, tc.msg, msg)
			assert.Equal(t, tc.outputTP2Command, genTPCmd)
		})
	}
}
