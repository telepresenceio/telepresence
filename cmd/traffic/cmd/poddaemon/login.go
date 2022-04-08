package poddaemon

import (
	"context"

	"github.com/telepresenceio/telepresence/v2/pkg/client/userd/auth/authdata"
)

type PoddLoginExecutor struct {
	key string
}

func (PoddLoginExecutor) Worker(ctx context.Context) error {
	panic("unimplemented: worker")
}

func (PoddLoginExecutor) Login(ctx context.Context) error {
	panic("unimplemented: login")
}

func (PoddLoginExecutor) LoginAPIKey(ctx context.Context, key string) (bool, error){
	panic("unimplmented: loginapikey")
}

func (PoddLoginExecutor) Logout(ctx context.Context) error {
	panic("unimplemented: logout")
}

func (p PoddLoginExecutor) GetAPIKey(ctx context.Context, description string) (string, error) {
	return p.key, nil
}

func (PoddLoginExecutor) GetLicense(ctx context.Context, id string) (string, string, error) {
	panic("unimplemented: getlicense")
}

func (PoddLoginExecutor) GetUserInfo(ctx context.Context, refresh bool) (*authdata.UserInfo, error) {
	panic("unimplemented: getuserinfo")
}
