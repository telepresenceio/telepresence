package rootd

import (
	"context"
	"errors"
	"strings"

	"google.golang.org/grpc"

	"github.com/telepresenceio/telepresence/v2/pkg/client/cli/daemon"
	"github.com/telepresenceio/telepresence/v2/pkg/grpc/server"
)

// callerRule decides what a connection that doesn't own the session may do.
type callerRule int

const (
	// ownerOnly calls are refused for other connections while a session exists.
	ownerOnly callerRule = iota

	// anyCaller calls are allowed for every connection.
	anyCaller

	// selfChecked calls check the caller inside the handler.
	selfChecked
)

// callerRules lists the methods that differ from the default, which is ownerOnly. Connect and Status
// answer differently depending on the caller, so their handlers decide. Quit also decides in its handler,
// under the lock that Connect holds while it creates a session. SetLogLevel is allowed for every
// connection while no session exists, and ownerOnly while one does.
var callerRules = map[string]callerRule{ //nolint:gochecknoglobals // constant lookup table
	"Version": anyCaller,
	"Connect": selfChecked,
	"Status":  selfChecked,
	"Quit":    selfChecked,
}

func (s *service) unaryGuard(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	if err := s.checkCaller(ctx, info.FullMethod); err != nil {
		return nil, err
	}
	return handler(ctx, req)
}

func (s *service) streamGuard(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
	if err := s.checkCaller(ss.Context(), info.FullMethod); err != nil {
		return err
	}
	return handler(srv, ss)
}

func (s *service) checkCaller(ctx context.Context, fullMethod string) error {
	name := fullMethod
	if ix := strings.LastIndexByte(name, '/'); ix >= 0 {
		name = name[ix+1:]
	}
	if callerRules[name] != ownerOnly {
		return nil
	}
	s.sessionLock.RLock()
	defer s.sessionLock.RUnlock()
	if s.session == nil || s.isOwnerLocked(ctx) {
		return nil
	}
	return daemon.ErrRootDaemonInUse()
}

// isOwnerLocked reports whether the call identified by ctx arrived on the connection that created the
// session. The caller must hold sessionLock.
func (s *service) isOwnerLocked(ctx context.Context) bool {
	id, ok := server.ConnID(ctx)
	return ok && s.session != nil && id == s.ownerConn
}

// connClosed ends the session if the connection that created it was the one that closed.
func (s *service) connClosed(id uint64) {
	s.sessionLock.RLock()
	owned := s.session != nil && s.ownerConn == id
	var old *session
	if owned {
		old = s.session
		s.sessionCancel(errors.New("root daemon session owner disconnected"))
	}
	s.sessionLock.RUnlock()
	if owned {
		s.clearSession(old)
	}
}
