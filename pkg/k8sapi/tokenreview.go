package k8sapi

import (
	"context"
	"fmt"
	"slices"

	auth "k8s.io/api/authentication/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/datawire/dlib/dlog"
)

// VerifyTokenResult contains the result of a token verification.
type VerifyTokenResult struct {
	Username string
	Groups   []string
}

// VerifyToken verifies a bearer token using Kubernetes TokenReview API
// and returns information about the authenticated user including their groups.
func VerifyToken(ctx context.Context, token string) (*VerifyTokenResult, error) {
	if token == "" {
		return nil, fmt.Errorf("token is empty")
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
		return nil, fmt.Errorf("token review failed: %v", err)
	}

	if !result.Status.Authenticated {
		dlog.Infof(ctx, "Token authentication failed")
		return nil, fmt.Errorf("token is not authenticated")
	}

	dlog.Infof(ctx, "Token authenticated for user %s with groups %v", result.Status.User.Username, result.Status.User.Groups)
	return &VerifyTokenResult{
		Username: result.Status.User.Username,
		Groups:   result.Status.User.Groups,
	}, nil
}

// IsMemberOfGroup checks if a user belongs to a specific group.
func (vtr *VerifyTokenResult) IsMemberOfGroup(targetGroup string) bool {
	return slices.Contains(vtr.Groups, targetGroup)
}
