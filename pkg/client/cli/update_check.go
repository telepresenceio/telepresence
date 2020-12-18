package cli

import (
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/blang/semver"
	"github.com/spf13/cobra"

	"github.com/datawire/dlib/dtime"
	"github.com/datawire/telepresence2/pkg/client"
)

const checkDuration = 24 * time.Hour
const checkURL = "https://s3.amazonaws.com/datawire-static-files/tel2"
const binaryName = "telepresence"

type updateChecker struct {
	NextCheck map[string]time.Time `json:"next_check"`
	url       string
	cacheFile string
}

// newUpdateChecker returns a new update checker, possibly initialized from the users cache.
func newUpdateChecker(url string) (*updateChecker, error) {
	cache, err := client.CacheDir()
	if err != nil {
		return nil, err
	}
	ts := &updateChecker{url: url, cacheFile: filepath.Join(cache, "update-checks.json")}

	js, err := ioutil.ReadFile(ts.cacheFile)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, err
		}
		ts.NextCheck = make(map[string]time.Time)
		return ts, nil
	}
	if err = json.Unmarshal(js, ts); err != nil {
		return nil, err
	}
	return ts, nil
}

// updateCheck performs an update check for the telepresence binary on the current os/arch and
// prints a message on stdout if an update is available
func updateCheck(cmd *cobra.Command, _ []string) error {
	uc, err := newUpdateChecker(fmt.Sprintf("%s/%s/%s/stable.txt", checkURL, runtime.GOOS, runtime.GOARCH))
	if err != nil || !uc.timeToCheck() {
		return err
	}

	ourVersion := client.Semver()
	update, err := uc.updateAvailable(&ourVersion)
	if err != nil {
		// Failed to contact remote server. Next attempt is due in an hour
		return uc.storeNextCheck(time.Hour)
	}
	if update != nil {
		fmt.Fprintf(cmd.OutOrStdout(), "An update of %s from version %s to %s is available. Download it from %s/%s/%s/%s/%s\n",
			binaryName, &ourVersion, update,
			checkURL, runtime.GOOS, runtime.GOARCH, update, binaryName)
	}
	return uc.storeNextCheck(checkDuration)
}

func (uc *updateChecker) storeNextCheck(d time.Duration) error {
	uc.NextCheck[uc.url] = dtime.Now().Add(d)
	js, err := json.MarshalIndent(uc, "", "  ")
	if err != nil {
		// Internal error. The updateChecker struct cannot be marshalled.
		panic(err)
	}
	if err = ioutil.WriteFile(uc.cacheFile, js, 0600); err != nil {
		err = fmt.Errorf("unable to write update check cache %s: %v", uc.cacheFile, err)
	}
	return err
}

func (uc *updateChecker) updateAvailable(currentVersion *semver.Version) (*semver.Version, error) {
	resp, err := http.Get(uc.url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	lastVersion, err := semver.Parse(string(body))
	if err != nil {
		return nil, err
	}
	if currentVersion.LT(lastVersion) {
		return &lastVersion, nil
	}
	return nil, nil
}

func (uc *updateChecker) timeToCheck() bool {
	ts, ok := uc.NextCheck[uc.url]
	return !ok || dtime.Now().After(ts)
}
