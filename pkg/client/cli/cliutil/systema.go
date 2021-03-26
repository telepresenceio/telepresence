package cliutil

import (
	"context"
	"errors"

	grpcCodes "google.golang.org/grpc/codes"
	grpcStatus "google.golang.org/grpc/status"
	empty "google.golang.org/protobuf/types/known/emptypb"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
	"github.com/telepresenceio/telepresence/v2/pkg/client/connector/auth/authdata"
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
	token, _ := authdata.LoadTokenFromUserCache(ctx)
	return token != nil
}

func GetCloudAccessToken(ctx context.Context) (string, error) {
	var tokenData *connector.TokenData
	err := WithConnector(ctx, func(ctx context.Context, connectorClient connector.ConnectorClient) error {
		var err error
		tokenData, err = connectorClient.GetCloudAccessToken(ctx, &empty.Empty{})
		return err
	})
	if err != nil {
		return "", err
	}
	return tokenData.GetAccessToken(), nil
}
