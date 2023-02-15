//go:build docker
// +build docker

package remotefs

import (
	"context"
	"errors"

	"github.com/datawire/go-fuseftp/rpc"
)

type fuseFtpMgr struct{}

type FuseFTPManager interface {
	DeferInit(ctx context.Context) error
	GetFuseFTPClient(ctx context.Context) rpc.FuseFTPClient
}

func NewFuseFTPManager() FuseFTPManager {
	return &fuseFtpMgr{}
}

func (s *fuseFtpMgr) DeferInit(ctx context.Context) error {
	return errors.New("fuseftp client is not available")
}

func (s *fuseFtpMgr) GetFuseFTPClient(ctx context.Context) rpc.FuseFTPClient {
	return nil
}
