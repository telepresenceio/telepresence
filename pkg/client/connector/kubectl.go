package connector

import (
	"context"
	"encoding/json"

	"github.com/blang/semver"
	"github.com/pkg/errors"

	"github.com/datawire/dlib/dexec"
)

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
