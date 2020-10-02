package daemon

import (
	"fmt"
	"io/ioutil"
	"time"

	"github.com/pkg/errors"

	"github.com/datawire/ambassador/internal/pkg/edgectl"
	"github.com/datawire/ambassador/pkg/supervisor"
)

// makeNetOverride sets up the network override resource for the daemon
func (d *service) makeNetOverride(p *supervisor.Process) error {
	netOverride, err := edgectl.CheckedRetryingCommand(
		p,
		"netOverride",
		edgectl.GetExe(),
		[]string{"teleproxy", "intercept", d.dns, d.fallback},
		d.checkNetOverride,
		10*time.Second,
	)
	if err != nil {
		return errors.Wrap(err, "teleproxy initial launch")
	}
	d.network = netOverride
	return nil
}

// checkNetOverride checks the status of teleproxy intercept by doing the
// equivalent of curl http://teleproxy/api/tables/.
func (d *service) checkNetOverride(_ *supervisor.Process) error {
	res, err := d.hClient.Get(fmt.Sprintf(
		"http://teleproxy%d.cachebust.telepresence.io/api/tables",
		time.Now().Unix(),
	))
	if err != nil {
		return err
	}
	_, err = ioutil.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		return err
	}
	return nil
}
