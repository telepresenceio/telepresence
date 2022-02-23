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

	"github.com/datawire/dlib/dexec"
	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client"
	"github.com/telepresenceio/telepresence/v2/pkg/client/errcat"
	"github.com/telepresenceio/telepresence/v2/pkg/client/scout"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/auth/authdata"
	"github.com/telepresenceio/telepresence/v2/pkg/filelocation"
)

// EnsureLoggedIn ensures that the user is logged in to Ambassador Cloud.  An error is returned if
// login fails.  The result code will indicate if this is a new login or if it resued an existing
// login.  If the `apikey` argument is empty an interactive login is performed; if it is non-empty
// the key is used instead of performing an interactive login.
func EnsureLoggedIn(ctx context.Context, apikey string) (connector.LoginResult_Code, error) {
	err := GetTelepresencePro(ctx)
	if err != nil {
		return connector.LoginResult_UNSPECIFIED, err
	}
	var code connector.LoginResult_Code
	err = WithConnector(ctx, func(ctx context.Context, connectorClient connector.ConnectorClient) error {
		var err error
		code, err = ClientEnsureLoggedIn(ctx, apikey, connectorClient)
		return err
	})
	return code, err
}

// ClientEnsureLoggedIn is like EnsureLoggedIn but uses an already acquired ConnectorClient.
func ClientEnsureLoggedIn(ctx context.Context, apikey string, connectorClient connector.ConnectorClient) (connector.LoginResult_Code, error) {
	resp, err := connectorClient.Login(ctx, &connector.LoginRequest{
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
	err := WithConnector(ctx, func(ctx context.Context, connectorClient connector.ConnectorClient) error {
		_, err := connectorClient.Logout(ctx, &empty.Empty{})
		return err
	})
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
	err := WithConnector(ctx, func(ctx context.Context, connectorClient connector.ConnectorClient) error {
		_, err := connectorClient.Logout(ctx, &empty.Empty{})
		return err
	})
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
	var userInfo *connector.UserInfo
	err := WithConnector(ctx, func(ctx context.Context, connectorClient connector.ConnectorClient) error {
		var err error
		userInfo, err = connectorClient.GetCloudUserInfo(ctx, &connector.UserInfoRequest{
			AutoLogin: autoLogin,
			Refresh:   refresh,
		})
		return err
	})
	if err != nil {
		return nil, err
	}
	return userInfo, nil
}

func GetCloudAPIKey(ctx context.Context, description string, autoLogin bool) (string, error) {
	var keyData *connector.KeyData
	err := WithConnector(ctx, func(ctx context.Context, connectorClient connector.ConnectorClient) error {
		var err error
		keyData, err = connectorClient.GetCloudAPIKey(ctx, &connector.KeyRequest{
			AutoLogin:   autoLogin,
			Description: description,
		})
		return err
	})
	if err != nil {
		return "", err
	}
	return keyData.GetApiKey(), nil
}

// GetCloudLicense communicates with system a to get the jwt version of the
// license, puts it in a kubernetes secret, and then writes that secret to the
// output file for the user to apply to their cluster
func GetCloudLicense(ctx context.Context, outputFile, id string) (string, string, error) {
	var licenseData *connector.LicenseData
	err := WithConnector(ctx, func(ctx context.Context, connectorClient connector.ConnectorClient) error {
		var err error
		licenseData, err = connectorClient.GetCloudLicense(ctx, &connector.LicenseRequest{
			Id: id,
		})
		return err
	})
	if err != nil {
		return "", "", err
	}
	return licenseData.GetLicense(), licenseData.GetHostDomain(), nil
}

func telProBinary(ctx context.Context) (string, error) {
	dir, err := filelocation.AppUserConfigDir(ctx)
	if err != nil {
		return "", errcat.NoDaemonLogs.Newf("unable to get path to config files: %w", err)
	}

	telProLocation := filepath.Join(dir, "telepresence-pro")
	if runtime.GOOS == "windows" {
		telProLocation += ".exe"
	}
	return telProLocation, nil
}

func checkProVersion(ctx context.Context, telProLocation string) (bool, error) {
	proCmd := dexec.CommandContext(ctx, telProLocation, "pro-version")
	proCmd.DisableLogging = true
	output, err := proCmd.CombinedOutput()
	if err != nil {
		return false, errcat.NoDaemonLogs.Newf("Unable to get telepresence pro version: %w", err)
	}
	return strings.Contains(string(output), client.Version()), nil
}

// GetTelepresencePro prompts the user to optionally install Telepresence Pro
// if it isn't installed. If the user installs it, it also asks the user to
// automatically update their configuration to use the new binary.
func GetTelepresencePro(ctx context.Context) (err error) {
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
		return err
	}

	if _, err := os.Stat(telProLocation); err != nil {
		if !os.IsNotExist(err) {
			return err
		}
		sc.SetMetadatum(ctx, "first_install", true)
	} else {
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

	if err = installTelepresencePro(ctx, telProLocation); err != nil {
		return errcat.NoDaemonLogs.Newf("error installing updated enhanced free client: %w", err)
	}
	return updateConfig(ctx, telProLocation)
}

// installTelepresencePro installs the binary. Users should be asked for
// permission before using this function
func installTelepresencePro(ctx context.Context, telProLocation string) error {
	// We install the correct version of telepresence-pro based on
	// the OSS version that is associated with this client since
	// daemon versions need to match
	clientVersion := strings.Trim(client.Version(), "v")
	systemAHost := client.GetConfig(ctx).Cloud.SystemaHost
	downloadURL := fmt.Sprintf("https://%s/download/tel-pro/%s/%s/%s/latest/%s",
		systemAHost, runtime.GOOS, runtime.GOARCH, clientVersion, filepath.Base(telProLocation))

	resp, err := http.Get(downloadURL)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			err = errors.New(resp.Status)
		}
	}
	if err != nil {
		return errcat.NoDaemonLogs.Newf("unable to download the enhanced free client: %w", err)
	}

	// Disconnect before attempting to create the new file.
	wasRunning := false
	err = WithStartedConnector(ctx, false, func(ctx context.Context, connectorClient connector.ConnectorClient) error {
		wasRunning = true
		_, err := connectorClient.Quit(ctx, &empty.Empty{})
		return err
	})
	if err != nil && err != ErrNoUserDaemon {
		return err
	}
	replaceConnectorConn(ctx, nil)

	if err = downloadProDaemon("the enhanced free client", resp.Body, telProLocation); err != nil {
		return err
	}
	if wasRunning {
		// relaunch the connector
		var conn *grpc.ClientConn
		if conn, err = launchConnectorDaemon(ctx, telProLocation, true); err != nil {
			return err
		}
		replaceConnectorConn(ctx, conn)
	}
	return nil
}

// downloadProDaemon copies the from stream into a temporary file in the same directory as the
// designated binary, chmods it to an executable, removes the old binary, and then renames the
// temporary file as the new binary
func downloadProDaemon(downloadURL string, from io.Reader, telProLocation string) (err error) {
	dir := filepath.Dir(telProLocation)
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
	fmt.Printf("Downloading %s...", downloadURL)
	_, err = io.Copy(tmp, from)
	_ = tmp.Close() // Important to close here. Don't defer this one
	if err != nil {
		return errcat.NoDaemonLogs.Newf("unable to download the enhanced free client: %w", err)
	}
	fmt.Println("done")
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
