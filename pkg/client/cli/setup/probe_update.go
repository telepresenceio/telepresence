package setup

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"runtime"
	"strings"

	"github.com/blang/semver/v4"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/ann"
	"github.com/telepresenceio/telepresence/v2/pkg/version"
)

// maxUpdateBodyBytes bounds the read of the remote stable.txt so a
// misbehaving or malicious host can't stream unbounded data into the probe.
const maxUpdateBodyBytes = 256

// probeUpdate is P7: a best-effort fetch of the client's stable-release
// marker, compared against the running client's version. Any failure leaves
// only CheckError set; it never fails GatherFacts.
func (p *Prober) probeUpdate(ctx context.Context) UpdateFacts {
	facts := UpdateFacts{}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.updateCheckURL(), nil)
	if err != nil {
		facts.CheckError = err.Error()
		return facts
	}
	resp, err := p.httpClient().Do(req)
	if err != nil {
		facts.CheckError = err.Error()
		return facts
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		facts.CheckError = fmt.Sprintf("update check returned status %s", resp.Status)
		return facts
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxUpdateBodyBytes))
	if err != nil {
		facts.CheckError = err.Error()
		return facts
	}
	latest, err := semver.ParseTolerant(strings.TrimSpace(string(body)))
	if err != nil {
		facts.CheckError = err.Error()
		return facts
	}
	if latest.GT(version.Structured) {
		facts.Latest = latest.String()
		facts.UpdateAvailable = true
	}
	return facts
}

// updateCheckURL builds the stable.txt URL. UpdateCheckHost is normally a bare
// host used with ann.Tel2's "https://" format; when it contains "://" (as a
// test server's URL does) it is used verbatim as the scheme+host prefix.
func (p *Prober) updateCheckURL() string {
	host := p.UpdateCheckHost
	if host == "" {
		host = defaultUpdateCheckHost
	}
	if strings.Contains(host, "://") {
		return fmt.Sprintf("%s/download/tel2/%s/%s/stable.txt", strings.TrimSuffix(host, "/"), runtime.GOOS, runtime.GOARCH)
	}
	return fmt.Sprintf(ann.Tel2, host, runtime.GOOS, runtime.GOARCH)
}
