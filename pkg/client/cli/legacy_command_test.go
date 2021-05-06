package cli

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
			msg:                "Telepresence 2 doesn't have methods. You can use --docker-run for container, otherwise tp2 works similarly to vpn-tcp",
		},
		{
			name:               "swapDeploymentUnknownParam",
			inputLegacyCommand: "telepresence --swap-deployment myserver --expose 9090 --not-real-param --run python3 -m http.server 9090",
			outputTP2Command:   "intercept myserver --port 9090 -- python3 -m http.server 9090",
			msg:                "The following flags used don't have a direct translation to tp2: --not-real-param",
		},
		{
			// This name isn't the greatest but basically, if we have an unknown
			// parameter in the process, that's fine because telepresence doesn't
			// care about parameters associated with a process a user is running.
			name:               "swapDeploymentUnknownParamInProcess",
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
			msg:                "This flag is ignored since Telepresence 2 uses one traffic-manager deployed in the ambassador namespace.",
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
			genTP2Cmd, msg, err := translateLegacyCmd(inputArgs)
			if err != nil {
				t.Fatal(err)
			}
			assert.Equal(t, tc.msg, msg)
			assert.Equal(t, tc.outputTP2Command, genTP2Cmd)
		})
	}
}
