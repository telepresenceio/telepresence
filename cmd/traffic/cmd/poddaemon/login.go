package poddaemon

import (
	"context"

	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/auth"
	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/auth/authdata"
)

// loginExecutor implements auth.LoginExecutor.  It mostly panics with "not implemented"; the only
// part of the login flow that the pod-daemon aught to be implementing is emitting an API key (which
// is simple and static, since the pod-daemon takes it an an env-var).
type loginExecutor struct {
	key string
}

var _ auth.LoginExecutor = loginExecutor{}

func (loginExecutor) Worker(ctx context.Context) error {
	panic("not implemented")
}

func (loginExecutor) Login(ctx context.Context) error {
	panic("not implemented")
}

func (loginExecutor) LoginAPIKey(ctx context.Context, key string) (bool, error) {
	panic("not implemented")
}

func (loginExecutor) Logout(ctx context.Context) error {
	panic("not implemented")
}

func (p loginExecutor) GetAPIKey(ctx context.Context, description string) (string, error) {
	return p.key, nil
}

func (loginExecutor) GetLicense(ctx context.Context, id string) (string, string, error) {
	panic("not implemented")
}

func (loginExecutor) GetUserInfo(ctx context.Context, refresh bool) (*authdata.UserInfo, error) {
	panic("not implemented")
}
