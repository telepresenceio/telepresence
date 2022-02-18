package cliutil

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	grpcCodes "google.golang.org/grpc/codes"
	grpcStatus "google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"
	"gopkg.in/yaml.v2"

	"github.com/datawire/dlib/dexec"
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

// GetTelepresencePro prompts the user to optionally install Telepresence Pro
// if it isn't installed. If the user installs it, it also asks the user to
// automatically update their configuration to use the new binary.
func GetTelepresencePro(ctx context.Context) error {
	dir, err := filelocation.AppUserConfigDir(ctx)
	if err != nil {
		return errcat.NoDaemonLogs.Newf("unable to get path to config files: %w", err)
	}

	sc := scout.NewReporter(ctx, "cli")
	sc.Start(ctx)
	defer sc.Close()
	installRefused := false
	defer func() {
		if err != nil {
			sc.Report(ctx, "pro_connector_upgrade_fail", scout.Entry{Key: "error", Value: err.Error()})
		} else if installRefused {
			sc.Report(ctx, "pro_connector_upgrade_refusal")
		} else {
			sc.Report(ctx, "pro_connector_upgrade_success")
		}
	}()

	// If telepresence-pro doesn't exist, then we should ask the user
	// if they want to install it
	telProLocation := filepath.Join(dir, "telepresence-pro")
	if runtime.GOOS == "windows" {
		telProLocation += ".exe"
	}
	if _, err = os.Stat(telProLocation); os.IsNotExist(err) {
		sc.SetMetadatum(ctx, "first_install", true)
		reader := bufio.NewReader(os.Stdin)
		fmt.Printf("Telepresence Pro is recommended when using login features, can Telepresence install it? (y/n)")
		var reply string
		reply, err = reader.ReadString('\n')
		if err != nil {
			return errcat.User.Newf("error reading input: %w", err)
		}

		// If the user doesn't want to install it, then we we'll proceed
		// with launching the daemon normally
		reply = strings.TrimSpace(reply)
		if reply != "y" {
			installRefused = true
			return nil
		}

		err = installTelepresencePro(ctx, telProLocation)
		if err != nil {
			return err
		}

		// Ask the user if they want to automatically update their config
		// with the telepresence-pro binary.
		// TODO: This will remove any comments that exist in the config file
		// which it's yaml so that's _fine_ but it would be nice if we didn't
		// do that.
		fmt.Printf("Update your Telepresence config to use Telepresence Pro? (y/n)")
		reply, err = reader.ReadString('\n')
		if err != nil {
			return errcat.User.Newf("error reading input: %w", err)
		}
		reply = strings.TrimSpace(reply)
		if reply != "y" {
			return nil
		}
		err = updateConfig(ctx, telProLocation)
		if err != nil {
			return err
		}
	} else {
		// If the binary is present, we check its version to ensure it's compatible
		// with the CLI
		sc.SetMetadatum(ctx, "first_install", false)
		proCmd := dexec.CommandContext(ctx, telProLocation, "pro-version")
		proCmd.DisableLogging = true

		var output []byte
		output, err = proCmd.CombinedOutput()
		if err != nil {
			return errcat.NoDaemonLogs.Newf("Unable to get telepresence pro version")
		}

		if !strings.Contains(string(output), client.Version()) {
			reader := bufio.NewReader(os.Stdin)
			fmt.Printf("Telepresence Pro needs to be upgraded to work with CLI version %s, allow Telepresence to upgrade it? (y/n)",
				client.Version())
			var reply string
			reply, err = reader.ReadString('\n')
			if err != nil {
				return errcat.NoDaemonLogs.Newf("error reading input: %w", err)
			}

			// If the user doesn't want to install it, then we we'll proceed
			// with launching the daemon normally
			reply = strings.TrimSpace(reply)
			if reply != "y" {
				installRefused = true
				return nil
			}
			err = os.Remove(telProLocation)
			if err != nil {
				return errcat.NoDaemonLogs.Newf("error removing Telepresence Pro: %w", err)
			}
			// Since we've already asked the user for permission to upgrade,
			// we can run these functions without asking permission again.
			err = installTelepresencePro(ctx, telProLocation)
			if err != nil {
				return errcat.NoDaemonLogs.Newf("error installing updated Telepresence Pro: %w",
					err)
			}

			// The users configuration is most likely correct if they are upgrading,
			// but we update it just to be extra sure.
			err = updateConfig(ctx, telProLocation)
			if err != nil {
				return errcat.NoDaemonLogs.Newf("error updating config: %w",
					err)
			}
		}
	}
	return nil
}

// installTelepresencePro installs the binary. Users should be asked for
// permission before using this function
func installTelepresencePro(ctx context.Context, telProLocation string) error {
	// We install the correct version of telepresence-pro based on
	// the OSS version that is associated with this client since
	// daemon versions need to match
	clientVersion := strings.Trim(client.Version(), "v")
	systemAHost := client.GetConfig(ctx).Cloud.SystemaHost
	installString := fmt.Sprintf("https://%s/download/tel-pro/%s/%s/%s/latest/%s",
		systemAHost, runtime.GOOS, runtime.GOARCH, clientVersion, filepath.Base(telProLocation))

	resp, err := http.Get(installString)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			err = errors.New(resp.Status)
		}
	}
	if err != nil {
		return errcat.NoDaemonLogs.Newf("unable to download Telepresence Pro: %w", err)
	}

	out, err := os.Create(telProLocation)
	if err != nil {
		return errcat.NoDaemonLogs.Newf("unable to create file %q for Telepresence Pro: %w", telProLocation, err)
	}
	defer out.Close()

	if _, err = io.Copy(out, resp.Body); err != nil {
		return errcat.NoDaemonLogs.Newf("unable to copy Telepresence Pro to %q: %w", telProLocation, err)
	}

	if err = os.Chmod(telProLocation, 0755); err != nil {
		return errcat.NoDaemonLogs.Newf("unable to set permissions of %q to 755: %w", telProLocation, err)
	}
	return nil
}

// updateConfig updates the userDaemonBinary in the config to point to
// telProLocation. Users should be asked for permission before this is done.
func updateConfig(ctx context.Context, telProLocation string) error {
	cfg := client.GetConfig(ctx)
	cfg.Daemons.UserDaemonBinary = telProLocation

	b, err := yaml.Marshal(cfg)
	if err != nil {
		return errcat.NoDaemonLogs.Newf("error marshaling updating config: %w", err)
	}
	cfgFile := client.GetConfigFile(ctx)
	_, err = os.OpenFile(cfgFile, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return errcat.NoDaemonLogs.Newf("error opening config file: %w", err)
	}
	err = os.WriteFile(cfgFile, b, 0644)
	if err != nil {
		return errcat.NoDaemonLogs.Newf("error writing config file: %w", err)
	}
	return nil
}
