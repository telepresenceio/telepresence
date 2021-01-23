package cli

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/blang/semver"

	"github.com/datawire/dlib/dtime"
	"github.com/datawire/telepresence2/v2/pkg/client/cache"
)

func newHttpServer(t *testing.T) *http.Server {
	addr, err := net.ResolveTCPAddr("tcp", "localhost:0")
	if err != nil {
		t.Helper()
		t.Fatal(err)
	}

	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		t.Helper()
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.NewServeMux()}
	srv.Addr = l.Addr().String()
	go func() { _ = srv.Serve(l) }()
	return srv
}

func tempDir(t *testing.T) string {
	tmpDir, err := ioutil.TempDir("", "update-test-")
	if err != nil {
		t.Helper()
		t.Fatal(err)
	}
	return tmpDir
}

func Test_newUpdateChecker(t *testing.T) {
	// the server that delivers the latest version
	httpServer := newHttpServer(t)
	defer httpServer.Close()

	// a fake user cache directory
	tmpDir := tempDir(t)
	defer func() {
		_ = os.RemoveAll(tmpDir)
	}()
	cache.SetUserCacheDirFunc(func() (string, error) {
		return tmpDir, nil
	})

	// request handler, returning the latest version
	lastestVer := semver.MustParse("1.2.3")
	httpServer.Handler.(*http.ServeMux).HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(lastestVer.String()))
	})

	ft := dtime.NewFakeTime()
	dtime.SetNow(ft.Now)

	uc, err := newUpdateChecker(fmt.Sprintf("http://%s", httpServer.Addr))
	if err != nil {
		t.Fatal(err)
	}
	// Virgin call should always trigger a check. Nothing is cached.
	if !uc.timeToCheck() {
		t.Fatal("Expected timeToCheck() to return true")
	}

	// An update to latestVer should be available
	currentVer := semver.MustParse("1.2.2")
	errOut := &bytes.Buffer{}
	v, _ := uc.updateAvailable(&currentVer, errOut)
	if len(errOut.Bytes()) > 0 {
		t.Fatal(errOut.String())
	}
	if v == nil || !lastestVer.EQ(*v) {
		t.Fatal(fmt.Sprintf("Expected updateAvailable() to return %s", lastestVer))
	}

	// create the initial cache.
	if err = uc.storeNextCheck(checkDuration); err != nil {
		t.Fatal(err)
	}

	// An hour later it should not be time to check yet
	ft.Step(time.Hour)
	uc, err = newUpdateChecker(fmt.Sprintf("http://%s", httpServer.Addr))
	if err != nil {
		t.Fatal(err)
	}
	if uc.timeToCheck() {
		t.Fatal("Expected timeToCheck() to return false")
	}

	// An day later it should be time to check
	ft.Step(checkDuration)
	uc, err = newUpdateChecker(fmt.Sprintf("http://%s", httpServer.Addr))
	if err != nil {
		t.Fatal(err)
	}
	if !uc.timeToCheck() {
		t.Fatal("Expected timeToCheck() to return true")
	}

	// No updates available
	currentVer = lastestVer
	v, _ = uc.updateAvailable(&currentVer, errOut)
	if len(errOut.Bytes()) > 0 {
		t.Fatal(errOut.String())
	}
	if v != nil {
		t.Fatal("Expected updateAvailable() to return nil")
	}
	if err = uc.storeNextCheck(checkDuration); err != nil {
		t.Fatal(err)
	}

	currentVer = lastestVer
	lastestVer = semver.MustParse("1.2.4")

	// An day later it should be time to check again
	ft.Step(checkDuration + 1)
	uc, err = newUpdateChecker(fmt.Sprintf("http://%s", httpServer.Addr))
	if err != nil {
		t.Fatal(err)
	}
	if !uc.timeToCheck() {
		t.Fatal("Expected timeToCheck() to return true")
	}

	// An update should be available
	v, _ = uc.updateAvailable(&currentVer, errOut)
	if len(errOut.Bytes()) > 0 {
		t.Fatal(errOut.String())
	}
	if v == nil || !lastestVer.EQ(*v) {
		t.Fatal(fmt.Sprintf("Expected updateAvailable() to return %s", lastestVer))
	}
}
