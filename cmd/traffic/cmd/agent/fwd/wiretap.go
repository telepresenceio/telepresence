package fwd

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/telepresenceio/dlib/v2/dlog"
)

// addConnectionTaps installs wiretaps on a connection. The wiretapped connection is returned along with the wiretaps.
// If count is zero, then this function returns the original connection and nil.
//
// A wiretap will receive all data read on the original connection and has the following characteristics:
//
//   - It is buffered. Each call to Read on the original connection creates one entry in the cache.
//   - Each Read on the wiretap consumes one entry in the cache.
//   - A wiretap will never cause the tapped connection to block. Data is discarded when the cache is full.
//   - Errors occurring when sending wiretapped data will cause the wiretap to close and be discarded.
//   - Errors occurring when reading on the original connection will be propagated and cause all wiretaps to be closed.
//
// The wiretap connection has the following characteristics:
//
//   - A Read will return the same as a Read on the original connection.
//   - A Write will silently discard the data to be written.
//   - SetTimeout, SetReadTimeout, and SetWriteTimeout are all no-ops.
//   - The LocalAddr and RemoteAddr calls are dispatched to the original connection.
//   - The connection is closed on error reading the original connection, when the context is done, or when the
//     original connection is explicitly closed.
func addConnectionTaps(ctx context.Context, conn net.Conn, count, cacheSize int) (net.Conn, []net.Conn) {
	if count == 0 {
		return conn, nil
	}

	tc := &teeConn{
		Conn: conn,
		cs:   make([]chan readResult, count),
	}
	taps := make([]net.Conn, count)

	for i := 0; i < count; i++ {
		rd, wr := io.Pipe()
		taps[i] = &readOverrideConn{Conn: conn, rd: rd}
		c := make(chan readResult, cacheSize)
		tc.cs[i] = c
		go writePump(ctx, c, wr)
	}
	return tc, taps
}

// readConn wraps an io.ReadCloser in a net.Conn. Writes are discarded and addresses are
// retrieved from the wrapped Conn.
type readOverrideConn struct {
	net.Conn
	rd io.ReadCloser
}

// Read reads from the wrapped io.ReadCloser.
func (e *readOverrideConn) Read(b []byte) (n int, err error) {
	return e.rd.Read(b)
}

// Write discards the data and returns its length.
func (e *readOverrideConn) Write(b []byte) (n int, err error) {
	return len(b), nil
}

// Close is a no-op. The real close must be made on the teeConn.
func (e *readOverrideConn) Close() error {
	return nil
}

// SetDeadline is a no-op.
func (e *readOverrideConn) SetDeadline(t time.Time) error {
	return nil
}

// SetReadDeadline is a no-op.
func (e *readOverrideConn) SetReadDeadline(t time.Time) error {
	return nil
}

// SetWriteDeadline is a no-op.
func (e *readOverrideConn) SetWriteDeadline(t time.Time) error {
	return nil
}

type readResult struct {
	data []byte
	err  error
}

// teeConn wraps a net.Conn and copies everything read from it to its pipe-writers
// using channel buffers.
// The teeConn is designed to discard data when the buffers fill up rather than
// causing a block.
// Any error encountered during Read will be propagated to the pipe-writers in a
// CloseWithError call.
type teeConn struct {
	net.Conn
	cs []chan readResult
}

func (mw *teeConn) Read(b []byte) (n int, err error) {
	n, err = mw.Conn.Read(b)
	rr := readResult{err: err}
	if n > 0 {
		rr.data = make([]byte, n)
		copy(rr.data, b)
	}
	mw.send(rr)
	return n, err
}

func (mw *teeConn) Close() error {
	mw.send(readResult{err: io.EOF})
	return mw.Conn.Close()
}

func (mw *teeConn) send(rr readResult) {
	for _, c := range mw.cs {
		select {
		case c <- rr:
		default:
			// We end up discarding data here if the consumer is too slow.
		}
	}
}

// teeReader wraps an io.ReadCloser and copies everything read from it to its pipe-writers
// using channel buffers.
// The teeReader is designed to discard data when the buffers fill up rather than
// causing a block.
// Any error encountered during Read will be propagated to the pipe-writers in a
// CloseWithError call.
type teeReader struct {
	io.ReadCloser
	cs []chan readResult
}

func (mw *teeReader) Read(b []byte) (n int, err error) {
	n, err = mw.ReadCloser.Read(b)
	rr := readResult{err: err}
	if n > 0 {
		rr.data = make([]byte, n)
		copy(rr.data, b)
	}
	mw.send(rr)
	return n, err
}

func (mw *teeReader) Close() error {
	mw.send(readResult{err: io.EOF})
	return mw.ReadCloser.Close()
}

func (mw *teeReader) send(rr readResult) {
	for _, c := range mw.cs {
		select {
		case c <- rr:
		default:
			// We end up discarding data here if the consumer is too slow.
		}
	}
}

func writePump(ctx context.Context, ch <-chan readResult, w *io.PipeWriter) {
	for {
		select {
		case <-ctx.Done():
			_ = w.CloseWithError(io.EOF)
			return
		case rr := <-ch:
			if len(rr.data) > 0 {
				n, err := w.Write(rr.data)
				if err == nil && n != len(rr.data) {
					err = io.ErrShortWrite
				}
				if err != nil {
					dlog.Errorf(ctx, "failed to write wiretap data: %v", err)
					return
				}
			}
			if rr.err != nil {
				_ = w.CloseWithError(rr.err)
				return
			}
		}
	}
}

// addRequestTaps installs wiretaps on a request. The readers for the taps are returned.
//
// A wiretap will receive the request header and all data read from its body and has the following characteristics:
//
//   - It is buffered. Each call to Read on the tapped request-body creates one entry in the cache.
//   - Each Read on the wiretap consumes one entry in the cache.
//   - A wiretap will never cause the reads on the tapped request-body to block. Data is discarded when the cache is full.
//   - Errors occurring when sending wiretapped data will cause the wiretap to close and be discarded.
//   - Errors occurring when reading on the tapped request-body will be propagated and cause all wiretaps to be closed.
//
// The wiretap reader has the following characteristics:
//
//   - A Read will return the same as a Read on the tapped request-body.
//   - The reader is closed on error reading the tapped request-body, when the context is done, or when the
//     tapped request-body is explicitly closed.

func addRequestTaps(ctx context.Context, request *http.Request, count, cacheSize int) ([]io.Reader, error) {
	if count == 0 {
		return nil, nil
	}

	body := request.Body
	tc := &teeReader{
		ReadCloser: body,
		cs:         make([]chan readResult, count),
	}
	taps := make([]io.Reader, count)

	// Write only the request header to the tap pipes. The body will be propagated by the teeReader.
	headerBuf := new(bytes.Buffer)
	request.Body = emptyReader{}
	err := request.Write(headerBuf)
	if err != nil {
		// Restore the original body
		request.Body = body
		return nil, err
	}

	// Replace the body with the teeReader.
	request.Body = tc

	headerData := headerBuf.Bytes()
	for i := 0; i < count; i++ {
		rd, wr := io.Pipe()
		taps[i] = rd
		c := make(chan readResult, cacheSize)
		tc.cs[i] = c
		go func() {
			_, err = wr.Write(headerData)
			if err != nil {
				dlog.Errorf(ctx, "Failed to write request to tap: %v", err)
			} else {
				writePump(ctx, c, wr)
			}
		}()
	}
	return taps, nil
}

type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (emptyReader) Close() error {
	return nil
}
