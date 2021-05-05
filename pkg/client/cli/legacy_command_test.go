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
		unknownFlags       []string
	}
	testCases := []testcase{
		{
			name:               "basicSwapDeployment",
			inputLegacyCommand: "telepresence --swap-deployment myserver --expose 9090 --run python3 -m http.server 9090",
			outputTP2Command:   "intercept myserver --port 9090 -- python3 -m http.server 9090",
		},
		{
			name:               "swapDeploymentUnknownParam",
			inputLegacyCommand: "telepresence --swap-deployment myserver --expose 9090 --not-real-param --run python3 -m http.server 9090",
			outputTP2Command:   "intercept myserver --port 9090 -- python3 -m http.server 9090",
			unknownFlags:       []string{"--not-real-param"},
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
			name:               "swapDeploymentBasicDockerRun",
			inputLegacyCommand: "telepresence --swap-deployment myserver --expose 80 --docker-run -i -t nginx:latest",
			outputTP2Command:   "intercept myserver --port 80 --docker-run -- -i -t nginx:latest",
		},
	}

	for _, tc := range testCases {
		tcName := tc.name
		tc := tc
		t.Run(tcName, func(t *testing.T) {
			inputArgs := strings.Split(tc.inputLegacyCommand, " ")
			genTP2Cmd, unknownFlags, err := translateLegacyCmd(inputArgs)
			if err != nil {
				t.Fatal(err)
			}
			assert.Equal(t, tc.unknownFlags, unknownFlags)
			assert.Equal(t, tc.outputTP2Command, genTP2Cmd)
		})
	}
}
