package cliutil

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"google.golang.org/grpc"
	grpcCodes "google.golang.org/grpc/codes"
	grpcStatus "google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"
	"gopkg.in/yaml.v2"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/output"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/auth/authdata"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
	"github.com/telepresenceio/telepresence/v2/pkg/proc"
)

// EnsureLoggedIn ensures that the user is logged in to Ambassador Cloud.  An error is returned if
// login fails.  The result code will indicate if this is a new login or if it re-used an existing
// login.  If the `apikey` argument is empty an interactive login is performed; if it is non-empty
// the key is used instead of performing an interactive login.
func EnsureLoggedIn(ctx context.Context, apikey string) (connector.LoginResult_Code, error) {
	resp, err := GetUserDaemon(ctx).Login(ctx, &connector.LoginRequest{
		ApiKey: apikey,
	})
	if err != nil {
		if grpcStatus.Code(err) == grpcCodes.PermissionDenied {
			err = errcat.User.New(grpcStatus.Convert(err).Message())
		}
		return connector.LoginResult_UNSPECIFIED, err
	}
	return resp.GetCode(), nil
}

// Logout logs out of Ambassador Cloud.  Returns an error if not logged in.
func Logout(ctx context.Context) error {
	_, err := GetUserDaemon(ctx).Logout(ctx, &empty.Empty{})
	if grpcStatus.Code(err) == grpcCodes.NotFound {
		err = errcat.User.New(grpcStatus.Convert(err).Message())
	}
	if err != nil {
		return err
	}
	return nil
}

// EnsureLoggedOut ensures that the user is logged out of Ambassador Cloud.  Returns nil if not
// logged in.
func EnsureLoggedOut(ctx context.Context) error {
	_, err := GetUserDaemon(ctx).Logout(ctx, &empty.Empty{})
	if grpcStatus.Code(err) == grpcCodes.NotFound {
		err = nil
	}
	if err != nil {
		return err
	}
	return nil
}

// HasLoggedIn returns true if either the user has an active login session or an expired login
// session, and returns false if either the user has never logged in or has explicitly logged out.
func HasLoggedIn(ctx context.Context) bool {
	_, err := authdata.LoadUserInfoFromUserCache(ctx)
	return err == nil
}

func GetCloudUserInfo(ctx context.Context, autoLogin bool, refresh bool) (*connector.UserInfo, error) {
	userInfo, err := GetUserDaemon(ctx).GetCloudUserInfo(ctx, &connector.UserInfoRequest{
		AutoLogin: autoLogin,
		Refresh:   refresh,
	})
	if err != nil {
		return nil, err
	}
	return userInfo, nil
}

// GetCloudLicense communicates with System A to get the jwt version of the
// license, puts it in a kubernetes secret, and then writes that secret to the
// output file for the user to apply to their cluster
func GetCloudLicense(ctx context.Context, outputFile, id string) (string, string, error) {
	licenseData, err := GetUserDaemon(ctx).GetCloudLicense(ctx, &connector.LicenseRequest{
		Id: id,
	})
	if err != nil {
		return "", "", err
	}
	return licenseData.GetLicense(), licenseData.GetHostDomain(), nil
}

func telProBinary(ctx context.Context) (string, error) {
	cfg := client.GetConfig(ctx)
	if cfg.Daemons.UserDaemonBinary != "" {
		dlog.Debugf(ctx, "Found configured UserDaemonBinary: %s", cfg.Daemons.UserDaemonBinary)
		return cfg.Daemons.UserDaemonBinary, nil
	}
	dir, err := filelocation.AppUserConfigDir(ctx)
	if err != nil {
		return "", errcat.NoDaemonLogs.Newf("unable to get path to config files: %w", err)
	}

	telProLocation := filepath.Join(dir, "telepresence-pro")
	dlog.Debugf(ctx, "No UserDaemonBinary found, looking in default location of %s", telProLocation)

	if runtime.GOOS == "windows" {
		telProLocation += ".exe"
	}
	return telProLocation, nil
}

func checkProVersion(ctx context.Context, telProLocation string) (bool, error) {
	proCmd := proc.CommandContext(ctx, telProLocation, "pro-version")
	proCmd.DisableLogging = true
	output, err := proCmd.CombinedOutput()
	if err != nil {
		dlog.Warnf(ctx, "failed to get telepresence pro version: %v", err)
		return false, errcat.NoDaemonLogs.Newf("Unable to get telepresence pro version: %w", err)
	}
	dlog.Debugf(ctx, "Telepresence pro: %s, CLI: %s", string(output), client.Version())
	return strings.Contains(string(output), client.Version()), nil
}

// getTelepresencePro prompts the user to optionally install Telepresence Pro
// if it isn't installed. If the user installs it, it also asks the user to
// automatically update their configuration to use the new binary.
func getTelepresencePro(ctx context.Context, userD connector.ConnectorClient) (err error) {
	dlog.Debugf(ctx, "Checking for telepresence-pro")
	sc := scout.NewReporter(ctx, "cli")
	sc.Start(ctx)
	defer sc.Close()

	defer func() {
		switch {
		case err != nil:
			sc.Report(ctx, "pro_connector_upgrade_fail", scout.Entry{Key: "error", Value: err.Error()})
		default:
			sc.Report(ctx, "pro_connector_upgrade_success")
		}
	}()

	var telProLocation string
	if telProLocation, err = telProBinary(ctx); err != nil {
		dlog.Warnf(ctx, "error from detecting telerpesence pro binary path: %v", err)
		return err
	}

	if _, err := os.Stat(telProLocation); err != nil {
		if !os.IsNotExist(err) {
			dlog.Warnf(ctx, "Error getting file with telepresence-pro; stat %s: %v", telProLocation, err)
			return err
		}
		dlog.Debugf(ctx, "telepresence pro does not exist in %s; fetching", telProLocation)
		sc.SetMetadatum(ctx, "first_install", true)
	} else {
		dlog.Debugf(ctx, "telepresence-pro binary found at %s", telProLocation)
		// If the binary is present, we check its version to ensure it's compatible
		// with the CLI
		ok, err := checkProVersion(ctx, telProLocation)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}

	if err = installTelepresencePro(ctx, telProLocation, userD); err != nil {
		return errcat.NoDaemonLogs.Newf("error installing updated enhanced free client: %w", err)
	}
	return updateConfig(ctx, telProLocation)
}

// installTelepresencePro installs the binary. Users should be asked for
// permission before using this function
func installTelepresencePro(ctx context.Context, telProLocation string, userD connector.ConnectorClient) error {
	// We install the correct version of telepresence-pro based on
	// the OSS version that is associated with this client since
	// daemon versions need to match
	clientVersion := strings.Trim(client.Version(), "v")
	systemAHost := client.GetConfig(ctx).Cloud.SystemaHost
	downloadURL := fmt.Sprintf("https://%s/download/tel-pro/%s/%s/%s/latest/%s",
		systemAHost, runtime.GOOS, runtime.GOARCH, clientVersion, filepath.Base(telProLocation))

	dlog.Debugf(ctx, "About to download telepresence-pro from %s", downloadURL)
	resp, err := http.Get(downloadURL)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			err = errors.New(resp.Status)
		}
	}
	if err != nil {
		dlog.Errorf(ctx, "Failed to download telepresence pro: %v", err)
		return errcat.NoDaemonLogs.Newf("unable to download the enhanced free client: %w", err)
	}

	// Disconnect before attempting to create the new file.
	if _, err := userD.Quit(ctx, &empty.Empty{}); err != nil {
		return err
	}
	if err != nil && err != ErrNoUserDaemon {
		return err
	}
	if err = downloadProDaemon(ctx, "the enhanced free client", resp.Body, telProLocation); err != nil {
		return err
	}
	// relaunch the connector
	var conn *grpc.ClientConn
	if conn, err = launchConnectorDaemon(ctx, telProLocation, true); err != nil {
		return err
	}
	replaceUserDaemon(ctx, conn)
	return nil
}

// downloadProDaemon copies the 'from' stream into a temporary file in the same directory as the
// designated binary, chmods it to be executable, removes the old binary, and then renames the
// temporary file as the new binary
func downloadProDaemon(ctx context.Context, downloadURL string, from io.Reader, telProLocation string) (err error) {
	stdout, _ := output.Structured(ctx)
	dir := filepath.Dir(telProLocation)
	if err = os.MkdirAll(dir, 0777); err != nil {
		return errcat.NoDaemonLogs.Newf("error creating directory %q: %w", dir, err)
	}

	name := filepath.Base(telProLocation)
	var tmp *os.File
	if tmp, err = os.CreateTemp(dir, name); err != nil {
		return errcat.NoDaemonLogs.Newf("unable to create temporary file in %q: %w", dir, err)
	}
	tmpName := tmp.Name()

	// Remove temp file if it still exists when we exit this function
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()

	// Perform the actual download
	fmt.Fprintf(stdout, "Downloading %s...", downloadURL)
	_, err = io.Copy(tmp, from)
	_ = tmp.Close() // Important to close here. Don't defer this one
	if err != nil {
		return errcat.NoDaemonLogs.Newf("unable to download the enhanced free client: %w", err)
	}
	fmt.Fprintln(stdout, "done")
	if err = os.Chmod(tmpName, 0755); err != nil {
		return errcat.NoDaemonLogs.Newf("unable to set permissions of %q to 755: %w", telProLocation, err)
	}
	if err = os.Remove(telProLocation); err != nil && !os.IsNotExist(err) {
		return errcat.NoDaemonLogs.Newf("error removing %q: %w", telProLocation, err)
	}
	if err = os.Rename(tmpName, telProLocation); err != nil {
		return errcat.NoDaemonLogs.Newf("error renaming %q to %q: %w", tmpName, telProLocation, err)
	}
	return nil
}

// updateConfig updates the userDaemonBinary in the config to point to
// telProLocation. Users should be asked for permission before this is done.
func updateConfig(ctx context.Context, telProLocation string) error {
	cfg := client.GetConfig(ctx)
	if cfg.Daemons.UserDaemonBinary == telProLocation {
		return nil
	}

	cfgFile := client.GetConfigFile(ctx)
	if cfg.Daemons.UserDaemonBinary == "" {
		dlog.Infof(ctx, "Updating %s, setting Daemons.UserDaemonBinary to %s", cfgFile, telProLocation)
	} else {
		dlog.Infof(ctx, "Updating %s, changing Daemons.UserDaemonBinary from %s to %s", cfgFile, cfg.Daemons.UserDaemonBinary, telProLocation)
	}

	cfg.Daemons.UserDaemonBinary = telProLocation
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return errcat.NoDaemonLogs.Newf("error marshaling updating config: %w", err)
	}
	if s, err := os.Stat(cfgFile); err == nil && s.Size() > 0 {
		_ = os.Rename(cfgFile, cfgFile+".bak")
	}

	f, err := os.OpenFile(cfgFile, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return errcat.NoDaemonLogs.Newf("error opening config file: %w", err)
	}
	defer f.Close()
	if _, err = f.Write(b); err != nil {
		return errcat.NoDaemonLogs.Newf("error writing config file: %w", err)
	}
	return nil
}
