package k8sapi

import (
	"context"
	"fmt"

	auth "k8s.io/api/authentication/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/datawire/dlib/dlog"
)

// VerifyTokenResult contains the result of a token verification.
type VerifyTokenResult struct {
	Authenticated bool
	Username      string
	Groups        []string
	Error         error
}

// VerifyToken verifies a bearer token using Kubernetes TokenReview API
// and returns information about the authenticated user including their groups.
func VerifyToken(ctx context.Context, token string) *VerifyTokenResult {
	if token == "" {
		return &VerifyTokenResult{
			Authenticated: false,
			Error:         fmt.Errorf("token is empty"),
		}
	}

	authHandler := GetK8sInterface(ctx).AuthenticationV1().TokenReviews()
	review := &auth.TokenReview{
		Spec: auth.TokenReviewSpec{
			Token: token,
		},
	}

	result, err := authHandler.Create(ctx, review, meta.CreateOptions{})
	if err != nil {
		dlog.Errorf(ctx, "TokenReview API call failed: %v", err)
		return &VerifyTokenResult{
			Authenticated: false,
			Error:         fmt.Errorf("token review failed: %v", err),
		}
	}

	if !result.Status.Authenticated {
		dlog.Infof(ctx, "Token authentication failed")
		return &VerifyTokenResult{
			Authenticated: false,
			Error:         fmt.Errorf("token is not authenticated"),
		}
	}

	dlog.Infof(ctx, "Token authenticated for user %s with groups %v", result.Status.User.Username, result.Status.User.Groups)
	return &VerifyTokenResult{
		Authenticated: true,
		Username:      result.Status.User.Username,
		Groups:        result.Status.User.Groups,
		Error:         nil,
	}
}

// IsMemberOfGroup checks if a user belongs to a specific group.
func IsMemberOfGroup(groups []string, targetGroup string) bool {
	for _, group := range groups {
		if group == targetGroup {
			return true
		}
	}
	return false
}
