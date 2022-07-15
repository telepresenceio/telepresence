package ftp

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"sync/atomic"

	ftp "github.com/fclairamb/ftpserverlib"
	"github.com/spf13/afero"

	"github.com/datawire/dlib/dlog"
	"github.com/telepresenceio/telepresence/v2/pkg/agentconfig"
)

type user struct {
	password string
	basePath string
}

type driver struct {
	ftp.Settings
	ctx         context.Context
	clientCount int32
	users       map[string]*user
}

type client struct {
	afero.Fs
}

type dbgListener struct {
	wl  net.Listener
	ctx context.Context
}

type dbgConn struct {
	net.Conn
	ctx context.Context
}

func (d *dbgConn) Close() error {
	err := d.Conn.Close()
	if err != nil {
		dlog.Errorf(d.ctx, "Conn.Close() addr %s: %v", d.RemoteAddr(), err)
	} else {
		dlog.Debugf(d.ctx, "Conn.Close() addr %s", d.RemoteAddr())
	}
	return err
}

func (d *dbgListener) Accept() (net.Conn, error) {
	c, err := d.wl.Accept()
	if err != nil {
		dlog.Errorf(d.ctx, "Listener.Accept(): %v", err)
	} else {
		dlog.Debugf(d.ctx, "Listener.Accept(): returns conn from %s", c.RemoteAddr())
		c = &dbgConn{Conn: c, ctx: d.ctx}
	}
	return c, err
}

func (d *dbgListener) Close() error {
	err := d.wl.Close()
	if err != nil {
		dlog.Errorf(d.ctx, "Listener.Close(): %v", err)
	} else {
		dlog.Debug(d.ctx, "Listener.Close()")
	}
	return err
}

func (d *dbgListener) Addr() net.Addr {
	return d.wl.Addr()
}

func newDriver(ctx context.Context, publicHost string, users map[string]*user, portAnnounceCh chan<- uint16) (*driver, error) {
	lc := net.ListenConfig{}
	l, err := lc.Listen(ctx, "tcp", "0.0.0.0:0")
	if err != nil {
		return nil, err
	}
	a := l.Addr().(*net.TCPAddr)

	d := &driver{
		ctx:   ctx,
		users: users,
		Settings: ftp.Settings{
			Banner:              "Telepresence Traffic Agent",
			PublicHost:          publicHost,
			DefaultTransferType: ftp.TransferTypeBinary,
			EnableHASH:          true,
			Listener:            &dbgListener{wl: l, ctx: ctx},
			ListenAddr:          a.String(),
			IdleTimeout:         300,
		}}

	dlog.Infof(ctx, "FTP server listening on %s", d.ListenAddr)
	portAnnounceCh <- uint16(a.Port)
	return d, nil
}

func (d *driver) ClientConnected(cc ftp.ClientContext) (string, error) {
	count := atomic.AddInt32(&d.clientCount, 1)
	dlog.Infof(d.ctx, "Client connected, id %d, remoteAddr %s, client count %d", cc.ID(), cc.RemoteAddr(), count)
	cc.SetDebug(dlog.MaxLogLevel(d.ctx) >= dlog.LogLevelDebug)
	return "telepresence", nil
}

func (d *driver) ClientDisconnected(cc ftp.ClientContext) {
	count := atomic.AddInt32(&d.clientCount, -1)
	dlog.Infof(d.ctx, "Client disconnected, id %d, remoteAddr %s, client count %d", cc.ID(), cc.RemoteAddr(), count)
}

func (d *driver) AuthUser(_ ftp.ClientContext, userName, password string) (ftp.ClientDriver, error) {
	user, ok := d.users[userName]
	if !(ok && user.password == "*" || user.password == password) {
		return nil, errors.New("unknown user")
	}
	return &client{Fs: afero.NewBasePathFs(SymLinkResolvingFs(afero.NewOsFs()), user.basePath)}, nil
}

func (d *driver) GetTLSConfig() (*tls.Config, error) {
	return nil, errors.New("not enabled")
}

func (d *driver) GetSettings() (*ftp.Settings, error) {
	return &d.Settings, nil
}

func Server(ctx context.Context, podIP string, portAnnounceCh chan<- uint16) error {
	defer close(portAnnounceCh)
	users := map[string]*user{
		"anonymous": {
			password: "*",
			basePath: agentconfig.ExportsMountPoint,
		},
	}
	d, err := newDriver(ctx, podIP, users, portAnnounceCh)
	if err != nil {
		return err
	}
	s := ftp.NewFtpServer(d)
	s.Logger = Logger(ctx)
	go func() {
		<-ctx.Done()
		dlog.Infof(ctx, "Stopping FTP server")
		if err := s.Stop(); err != nil {
			dlog.Errorf(ctx, "failed to stop ftp server: %v", err)
		}
	}()
	dlog.Infof(ctx, "Starting FTP server")
	return s.ListenAndServe()
}
