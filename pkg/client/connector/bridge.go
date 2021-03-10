package connector

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/blang/semver"
	"github.com/pkg/errors"

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/daemon"
)

// bridge holds the configuration for a Teleproxy
type bridge struct {
	sshPort int32
	daemon  daemon.DaemonClient
	cancel  context.CancelFunc
}

func newBridge(daemon daemon.DaemonClient, sshPort int32) *bridge {
	return &bridge{
		daemon:  daemon,
		sshPort: sshPort,
	}
}

func (br *bridge) sshWorker(c context.Context) error {
	c, br.cancel = context.WithCancel(c)
	defer br.cancel()

	// XXX: probably need some kind of keepalive check for ssh, first
	// curl after wakeup seems to trigger detection of death
	ssh := dexec.CommandContext(c, "ssh",

		"-F", "none", // don't load the user's config file

		// connection settings
		"-C", // compression
		"-oConnectTimeout=5",
		"-oStrictHostKeyChecking=no",     // don't bother checking the host key...
		"-oUserKnownHostsFile=/dev/null", // and since we're not checking it, don't bother remembering it either

		// port-forward settings
		"-N", // no remote command; just connect and forward ports
		"-oExitOnForwardFailure=yes",
		"-D", "localhost:1080",

		// where to connect to
		"-p", strconv.Itoa(int(br.sshPort)),
		"telepresence@localhost",
	)
	err := ssh.Run()
	if err != nil && c.Err() != nil {
		err = nil
	}
	return err
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

// check checks the status of teleproxy bridge by doing the equivalent of
//  curl http://traffic-manager.svc:8022.
// Note there is no namespace specified, as we are checking for bridge status in the
// current namespace.
func (br *bridge) check(c context.Context) bool {
	if br == nil {
		return false
	}
	address := fmt.Sprintf("localhost:%d", br.sshPort)
	conn, err := net.DialTimeout("tcp", address, 15*time.Second)
	if err != nil {
		dlog.Errorf(c, "fail to establish tcp connection to %s: %v", address, err)
		return false
	}
	defer conn.Close()

	msg, _, err := bufio.NewReader(conn).ReadLine()
	if err != nil {
		dlog.Errorf(c, "tcp read: %v", err)
		return false
	}
	if !strings.Contains(string(msg), "SSH") {
		dlog.Errorf(c, "expected SSH prompt, got: %v", string(msg))
		return false
	}
	return true
}
