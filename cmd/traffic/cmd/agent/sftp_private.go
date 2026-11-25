package agent

import (
	"context"
	"errors"
	"io"
	"net"

	"github.com/telepresenceio/clog"
)

// openSftpConn returns one end of a connected pair whose other end is served by the
// agent's SFTP server for as long as ctx lives.
func (s *state) openSftpConn(ctx context.Context) (net.Conn, error) {
	if s.sftpSrv == nil {
		return nil, errors.New("sftp server is not running")
	}
	server, client, err := socketPair()
	if err != nil {
		return nil, err
	}
	go func() {
		defer server.Close()
		if err := s.sftpSrv.Serve(ctx, server); err != nil && !errors.Is(err, io.EOF) {
			clog.Errorf(ctx, "sftp server completed with error %v", err)
		}
	}()
	return client, nil
}
