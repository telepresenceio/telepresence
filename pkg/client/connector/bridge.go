package connector

import (
	"context"
	"encoding/json"

	"github.com/blang/semver"
	"github.com/pkg/errors"

	"github.com/datawire/dlib/dexec"
)

// bridge holds the configuration for a Teleproxy
type bridge struct {
	tm *trafficManager
}

func newBridge(tm *trafficManager) *bridge {
	return &bridge{
		tm: tm,
	}
}

// No return value because it always retries (until the Context is canceled) instead of returning an
// error.
func (br *bridge) sshWorker(ctx context.Context) {
	br.tm.sshPortForward(ctx,
		"-D", "localhost:1080",
	)
}

const kubectlErr = "kubectl version 1.10 or greater is required"

func checkKubectl(c context.Context) error {
	output, err := dexec.CommandContext(c, "kubectl", "version", "--client", "-o", "json").Output()
	if err != nil {
		return errors.Wrap(err, kubectlErr)
	}

	var info struct {
		ClientVersion struct {
			GitVersion string
		}
	}

	if err = json.Unmarshal(output, &info); err != nil {
		return errors.Wrap(err, kubectlErr)
	}

	version, err := semver.ParseTolerant(info.ClientVersion.GitVersion)
	if err != nil {
		return errors.Wrap(err, kubectlErr)
	}

	if version.Major != 1 || version.Minor < 10 {
		return errors.Errorf("%s (found %s)", kubectlErr, info.ClientVersion.GitVersion)
	}
	return nil
}
