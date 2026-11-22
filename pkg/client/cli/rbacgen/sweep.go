package rbacgen

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	"golang.org/x/sync/errgroup"

	auth "k8s.io/api/authorization/v1"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// reviewConcurrency bounds how many SelfSubjectAccessReviews Sweep issues at
// once, so a large chart render doesn't open hundreds of simultaneous requests.
const reviewConcurrency = 8

// Allowed reports whether the caller may perform ra, with one
// SelfSubjectAccessReview.
func Allowed(ctx context.Context, client kubernetes.Interface, ra *auth.ResourceAttributes) (bool, error) {
	review := &auth.SelfSubjectAccessReview{Spec: auth.SelfSubjectAccessReviewSpec{ResourceAttributes: ra}}
	result, err := client.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, review, meta.CreateOptions{})
	if err != nil {
		return false, err
	}
	return result.Status.Allowed, nil
}

// Sweep reviews every attribute set for the caller, up to reviewConcurrency
// at a time, and returns the denied ones sorted by AttributeKey. It errors
// only when a review call itself fails, never on a denial.
func Sweep(ctx context.Context, client kubernetes.Interface, ras []*auth.ResourceAttributes) ([]*auth.ResourceAttributes, error) {
	var mu sync.Mutex
	var denied []*auth.ResourceAttributes
	var callErrs []string

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(reviewConcurrency)
	for _, ra := range ras {
		g.Go(func() error {
			allowed, err := Allowed(gctx, client, ra)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				callErrs = append(callErrs, err.Error())
			} else if !allowed {
				denied = append(denied, ra)
			}
			return nil
		})
	}
	_ = g.Wait()

	if len(callErrs) > 0 {
		return denied, errors.New(strings.Join(callErrs, "; "))
	}
	sort.Slice(denied, func(i, j int) bool { return AttributeKey(denied[i]) < AttributeKey(denied[j]) })
	return denied, nil
}
