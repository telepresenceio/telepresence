package cliutil

import (
	"context"
	"errors"

	grpcCodes "google.golang.org/grpc/codes"
	grpcStatus "google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/userd_auth/authdata"
)

// EnsureLoggedIn ensures that the user is logged in to Ambassador Cloud.  An error is returned if
// login fails.  The result code will indicate if this is a new login or if it resued an existing
// login.
func EnsureLoggedIn(ctx context.Context) (connector.LoginResult_Code, error) {
	var resp *connector.LoginResult
	err := WithConnector(ctx, func(ctx context.Context, connectorClient connector.ConnectorClient) error {
		var err error
		resp, err = connectorClient.Login(ctx, &empty.Empty{})
		return err
	})
	return resp.GetCode(), err
}

// Logout logs out of Ambassador Cloud.  Returns an error if not logged in.
func Logout(ctx context.Context) error {
	err := WithConnector(ctx, func(ctx context.Context, connectorClient connector.ConnectorClient) error {
		_, err := connectorClient.Logout(ctx, &empty.Empty{})
		return err
	})
	if grpcStatus.Code(err) == grpcCodes.NotFound {
		err = errors.New(grpcStatus.Convert(err).Message())
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
	userInfo, _ := authdata.LoadUserInfoFromUserCache(ctx)
	return userInfo != nil
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
