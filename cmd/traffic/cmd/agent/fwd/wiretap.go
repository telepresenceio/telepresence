package fwd

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/telepresenceio/clog"
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
//
// A teeReader's taps must not be relied on to terminate solely by the wrapped body being
// read to EOF: nothing guarantees the body is ever read at all (e.g. httputil.ReverseProxy
// never reads the body of a bodyless GET). Callers that install a teeReader on a request must
// therefore call closeTaps once the request has been fully served, regardless of whether the
// body was read. closeTaps is idempotent, so it is safe to call it again if the body does end
// up being read to completion or the server itself closes the body.
type teeReader struct {
	io.ReadCloser
	cs        []chan readResult
	closeOnce sync.Once
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

// closeTaps signals EOF to every tap pipe exactly once. It is safe to call multiple times and
// from multiple goroutines.
func (mw *teeReader) closeTaps() {
	mw.closeOnce.Do(func() {
		mw.send(readResult{err: io.EOF})
	})
}

func (mw *teeReader) Close() error {
	mw.closeTaps()
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
					clog.Errorf(ctx, "failed to write wiretap data: %v", err)
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

// addRequestTaps installs wiretaps on a request. The readers for the taps are returned, along
// with the teeReader that was installed as the request's body.
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
//
// Nothing guarantees that the tapped request-body is ever read (e.g. the body of a bodyless GET
// is never read by httputil.ReverseProxy), so the caller must call closeTaps on the returned
// teeReader once the request has been fully served, regardless of whether the body was read, or
// the taps will never see a terminating EOF.
func addRequestTaps(ctx context.Context, request *http.Request, count, cacheSize int) ([]io.Reader, *teeReader, error) {
	if count == 0 {
		return nil, nil, nil
	}

	body := request.Body
	tc := &teeReader{
		ReadCloser: body,
		cs:         make([]chan readResult, count),
	}
	taps := make([]io.Reader, count)

	// Write only the request header to the tap pipes. The body will be propagated by the
	// teeReader below.
	//
	// request.Write requires that the number of body bytes it writes matches a declared,
	// known Content-Length, so an empty stand-in body only works when Content-Length is
	// zero or unknown (chunked). When a positive length is declared, feed it that many
	// zero bytes instead so the write succeeds; headerCaptureWriter stops retaining data
	// once the header/body boundary is seen, so a large declared length is never buffered.
	hw := &headerCaptureWriter{}
	if cl := request.ContentLength; cl > 0 {
		request.Body = io.NopCloser(io.LimitReader(zeroReader{}, cl))
	} else {
		request.Body = emptyReader{}
	}
	err := request.Write(hw)
	// Restore the original body; it is only swapped for the teeReader below on success.
	request.Body = body
	if err != nil {
		return nil, nil, err
	}
	headerData := hw.buf.Bytes()

	// Replace the body with the teeReader.
	request.Body = tc

	for i := 0; i < count; i++ {
		rd, wr := io.Pipe()
		taps[i] = rd
		c := make(chan readResult, cacheSize)
		tc.cs[i] = c
		go func() {
			if _, err := wr.Write(headerData); err != nil {
				clog.Errorf(ctx, "Failed to write request to tap: %v", err)
			} else {
				writePump(ctx, c, wr)
			}
		}()
	}
	return taps, tc, nil
}

type emptyReader struct{}

func (emptyReader) Read([]byte) (int, error) {
	return 0, io.EOF
}

func (emptyReader) Close() error {
	return nil
}

// zeroReader is an endless source of zero bytes, used to synthesize a stand-in request body
// of a declared length without allocating it.
type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

// headerCaptureWriter retains everything written to it up to and including the first blank
// line (the request-line/header block of an HTTP/1.x message), then silently discards
// everything after it. This lets request.Write be fed a synthetic body of an arbitrary
// declared length, for Content-Length validation purposes, without that body ending up
// buffered in memory.
type headerCaptureWriter struct {
	buf       bytes.Buffer
	sawHeader bool
}

func (w *headerCaptureWriter) Write(p []byte) (int, error) {
	if w.sawHeader {
		return len(p), nil
	}
	n, err := w.buf.Write(p)
	if idx := bytes.Index(w.buf.Bytes(), []byte("\r\n\r\n")); idx >= 0 {
		w.sawHeader = true
		w.buf.Truncate(idx + 4)
	}
	return n, err
}
