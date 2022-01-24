package cli

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/blang/semver"

	"github.com/datawire/dlib/dcontext"
	"github.com/datawire/dlib/dhttp"
	"github.com/datawire/dlib/dlog"
	"github.com/datawire/dlib/dtime"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

func Test_NewUpdateChecker(t *testing.T) {
	ctx := dlog.NewTestContext(t, false)

	// the server that delivers the latest version
	l, err := net.Listen("tcp", "localhost:0")
	if err != nil {
		t.Fatal(err)
	}
	lastestVer := semver.MustParse("1.2.3")
	httpSrvCfg := &dhttp.ServerConfig{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(lastestVer.String()))
		}),
	}
	httpSrvCh := make(chan error)
	httpSrvCtx, httpSrvCancel := context.WithCancel(dcontext.WithSoftness(ctx))
	go func() {
		httpSrvCh <- httpSrvCfg.Serve(httpSrvCtx, l)
		close(httpSrvCh)
	}()
	defer func() {
		httpSrvCancel()
		if err := <-httpSrvCh; err != nil {
			t.Error(err)
		}
	}()

	// a fake user cache directory
	ctx = filelocation.WithUserHomeDir(ctx, t.TempDir())

	ft := dtime.NewFakeTime()
	dtime.SetNow(ft.Now)

	uc, err := NewUpdateChecker(ctx, fmt.Sprintf("http://%s", l.Addr()))
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
	v, _ := uc.UpdateAvailable(&currentVer, errOut)
	if len(errOut.Bytes()) > 0 {
		t.Fatal(errOut.String())
	}
	if v == nil || !lastestVer.EQ(*v) {
		t.Fatal(fmt.Sprintf("Expected updateAvailable() to return %s", lastestVer))
	}

	// create the initial cache.
	if err = uc.StoreNextCheck(ctx, checkDuration); err != nil {
		t.Fatal(err)
	}

	// An hour later it should not be time to check yet
	ft.Step(time.Hour)
	uc, err = NewUpdateChecker(ctx, fmt.Sprintf("http://%s", l.Addr()))
	if err != nil {
		t.Fatal(err)
	}
	if uc.timeToCheck() {
		t.Fatal("Expected timeToCheck() to return false")
	}

	// An day later it should be time to check
	ft.Step(checkDuration)
	uc, err = NewUpdateChecker(ctx, fmt.Sprintf("http://%s", l.Addr()))
	if err != nil {
		t.Fatal(err)
	}
	if !uc.timeToCheck() {
		t.Fatal("Expected timeToCheck() to return true")
	}

	// No updates available
	currentVer = lastestVer
	v, _ = uc.UpdateAvailable(&currentVer, errOut)
	if len(errOut.Bytes()) > 0 {
		t.Fatal(errOut.String())
	}
	if v != nil {
		t.Fatal("Expected updateAvailable() to return nil")
	}
	if err = uc.StoreNextCheck(ctx, checkDuration); err != nil {
		t.Fatal(err)
	}

	currentVer = lastestVer
	lastestVer = semver.MustParse("1.2.4")

	// An day later it should be time to check again
	ft.Step(checkDuration + 1)
	uc, err = NewUpdateChecker(ctx, fmt.Sprintf("http://%s", l.Addr()))
	if err != nil {
		t.Fatal(err)
	}
	if !uc.timeToCheck() {
		t.Fatal("Expected timeToCheck() to return true")
	}

	// An update should be available
	v, _ = uc.UpdateAvailable(&currentVer, errOut)
	if len(errOut.Bytes()) > 0 {
		t.Fatal(errOut.String())
	}
	if v == nil || !lastestVer.EQ(*v) {
		t.Fatal(fmt.Sprintf("Expected updateAvailable() to return %s", lastestVer))
	}
}
