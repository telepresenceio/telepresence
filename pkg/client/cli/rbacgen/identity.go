package rbacgen

import (
	"context"
	"fmt"
	"slices"
	"strings"

	authn "k8s.io/api/authentication/v1"
	rbac "k8s.io/api/rbac/v1"
	k8serrors "k8s.io/apimachinery/pkg/api/errors"
	meta "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const serviceAccountPrefix = "system:serviceaccount:"

// Subject names the subject of a generated binding.
type Subject struct {
	Kind      string
	Name      string
	Namespace string
}

// ParseSubject parses KIND:NAME, where KIND is User, Group or
// ServiceAccount, and the NAME of a ServiceAccount is NAMESPACE/NAME.
func ParseSubject(s string) (Subject, error) {
	kind, name, _ := strings.Cut(s, ":")
	switch kind {
	case rbac.UserKind, rbac.GroupKind:
		if name != "" {
			return Subject{Kind: kind, Name: name}, nil
		}
	case rbac.ServiceAccountKind:
		ns, n, ok := strings.Cut(name, "/")
		if ok && ns != "" && n != "" {
			return Subject{Kind: kind, Name: n, Namespace: ns}, nil
		}
	}
	return Subject{}, fmt.Errorf("invalid subject %q, must be User:NAME, Group:NAME or ServiceAccount:NAMESPACE/NAME", s)
}

func (s Subject) String() string {
	if s.Kind == rbac.ServiceAccountKind {
		return s.Kind + ":" + s.Namespace + "/" + s.Name
	}
	return s.Kind + ":" + s.Name
}

// RBAC returns the subject as it goes into a binding.
func (s Subject) RBAC() rbac.Subject {
	if s.Kind == rbac.ServiceAccountKind {
		return rbac.Subject{Kind: s.Kind, Name: s.Name, Namespace: s.Namespace}
	}
	return rbac.Subject{Kind: s.Kind, APIGroup: rbacGroup, Name: s.Name}
}

// asFlags returns the impersonation flags that check the subject.
func (s Subject) asFlags() string {
	switch s.Kind {
	case rbac.ServiceAccountKind:
		return "--as " + serviceAccountPrefix + s.Namespace + ":" + s.Name
	case rbac.GroupKind:
		return "--as <member> --as-group " + s.Name
	default:
		return "--as " + s.Name
	}
}

// Identity is the user that the API server sees for the caller.
type Identity struct {
	Username string
	Groups   []string
}

// ReadIdentity reads the caller's identity with a SelfSubjectReview. It
// returns nil, and no error, when the cluster doesn't serve the review
// (it needs Kubernetes 1.28) or refuses it.
func ReadIdentity(ctx context.Context, client kubernetes.Interface) (*Identity, error) {
	review, err := client.AuthenticationV1().SelfSubjectReviews().Create(ctx, &authn.SelfSubjectReview{}, meta.CreateOptions{})
	if err != nil {
		if k8serrors.IsNotFound(err) || k8serrors.IsForbidden(err) || k8serrors.IsMethodNotSupported(err) {
			return nil, nil
		}
		return nil, err
	}
	ui := review.Status.UserInfo
	if ui.Username == "" {
		return nil, nil
	}
	return &Identity{Username: ui.Username, Groups: ui.Groups}, nil
}

// Subject returns the identity as a ServiceAccount or User subject.
func (id *Identity) Subject() Subject {
	if rest, ok := strings.CutPrefix(id.Username, serviceAccountPrefix); ok {
		if ns, name, ok := strings.Cut(rest, ":"); ok {
			return Subject{Kind: rbac.ServiceAccountKind, Name: name, Namespace: ns}
		}
	}
	return Subject{Kind: rbac.UserKind, Name: id.Username}
}

// Matches reports whether s names the identity, or a group it belongs to.
func (id *Identity) Matches(s Subject) bool {
	switch s.Kind {
	case rbac.GroupKind:
		return slices.Contains(id.Groups, s.Name)
	case rbac.ServiceAccountKind:
		return id.Username == serviceAccountPrefix+s.Namespace+":"+s.Name
	default:
		return id.Username == s.Name
	}
}
