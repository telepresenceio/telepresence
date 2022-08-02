package cliutil

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/blang/semver"
	"github.com/spf13/cobra"

	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cache"
)

const checkDuration = 24 * time.Hour
const binaryName = "telepresence"
const cacheFilename = "update-checks.json"

type UpdateChecker struct {
	NextCheck map[string]time.Time `json:"next_check"`
	url       string
}

// newUpdateChecker returns a new update checker, possibly initialized from the users cache.
func NewUpdateChecker(ctx context.Context, url string) (*UpdateChecker, error) {
	ts := &UpdateChecker{
		url: url,
	}

	if err := cache.LoadFromUserCache(ctx, ts, cacheFilename); err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		ts.NextCheck = make(map[string]time.Time)
	}
	return ts, nil
}

func UpdateCheckIfDue(cmd *cobra.Command, _ []string) error {
	return updateCheck(cmd, false)
}

func ForcedUpdateCheck(cmd *cobra.Command, _ []string) error {
	return updateCheck(cmd, true)
}

// updateCheck performs an update check for the telepresence binary on the current os/arch and
// prints a message on stdout if an update is available.
//
// Arguments:
//   cmd:         the command that provides Context and stout/stderr
//   forcedCheck: if true, perform check regardless of if it's due or not
func updateCheck(cmd *cobra.Command, forceCheck bool) error {
	cloudCfg := client.GetConfig(cmd.Context()).Cloud
	uc, err := NewUpdateChecker(cmd.Context(), fmt.Sprintf("https://%s/download/tel2/%s/%s/stable.txt", cloudCfg.SystemaHost, runtime.GOOS, runtime.GOARCH))
	if err != nil || !(forceCheck || uc.timeToCheck()) {
		return err
	}

	ourVersion := client.Semver()
	update, ok := uc.UpdateAvailable(&ourVersion, cmd.ErrOrStderr())
	if !ok {
		// Failed to read from remote server. Next attempt is due in an hour
		return uc.StoreNextCheck(cmd.Context(), time.Hour)
	}
	if update != nil {
		fmt.Fprintf(cmd.OutOrStdout(),
			"An update of %s from version %s to %s is available. Please visit https://www.getambassador.io/docs/telepresence/latest/install/upgrade/ for more info.\n",
			binaryName, &ourVersion, update)
	}
	return uc.StoreNextCheck(cmd.Context(), checkDuration)
}

func (uc *UpdateChecker) StoreNextCheck(ctx context.Context, d time.Duration) error {
	uc.NextCheck[uc.url] = dtime.Now().Add(d)
	return cache.SaveToUserCache(ctx, uc, cacheFilename)
}

func (uc *UpdateChecker) UpdateAvailable(currentVersion *semver.Version, errOut io.Writer) (*semver.Version, bool) {
	resp, err := http.Get(uc.url)
	if err != nil {
		// silently ignore connection failures
		return nil, false
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		// silently ignore failure to read response body
		return nil, false
	}
	vs := strings.TrimSpace(string(body))
	lastVersion, err := semver.Parse(vs)
	if err != nil {
		// The version found remotely is invalid. Not fatal, but inform the user.
		fmt.Fprintf(errOut, "Update checker was unable to parse version %q returned from %s: %v\n", vs, uc.url, err)
		return nil, false
	}
	if currentVersion.LT(lastVersion) {
		return &lastVersion, true
	}
	return nil, true
}

func (uc *UpdateChecker) timeToCheck() bool {
	ts, ok := uc.NextCheck[uc.url]
	return !ok || dtime.Now().After(ts)
}
