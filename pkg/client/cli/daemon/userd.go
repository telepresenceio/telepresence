package daemon

import (
	"context"
	"strconv"
	"strings"

	"google.golang.org/grpc"

	"github.com/telepresenceio/telepresence/rpc/v2/connector"
)

type UserClient struct {
	connector.ConnectorClient
	Conn     *grpc.ClientConn
	DaemonID *Identifier
}

type Session struct {
	UserClient
	Info    *connector.ConnectInfo
	Started bool
}

type userDaemonKey struct{}

func GetUserClient(ctx context.Context) *UserClient {
	if ud, ok := ctx.Value(userDaemonKey{}).(*UserClient); ok {
		return ud
	}
	return nil
}

func WithUserClient(ctx context.Context, ud *UserClient) context.Context {
	return context.WithValue(ctx, userDaemonKey{}, ud)
}

type sessionKey struct{}

func GetSession(ctx context.Context) *Session {
	if s, ok := ctx.Value(sessionKey{}).(*Session); ok {
		return s
	}
	return nil
}

func WithSession(ctx context.Context, s *Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, s)
}

func (ud *UserClient) Containerized() bool {
	return ud.DaemonID.Containerized
}

func (ud *UserClient) DaemonPort() int {
	if ud.DaemonID.Containerized {
		addr := ud.Conn.Target()
		if lc := strings.LastIndexByte(addr, ':'); lc >= 0 {
			if port, err := strconv.Atoi(addr[lc+1:]); err == nil {
				return port
			}
		}
	}
	return -1
}
