package rabbitmq

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// mgmtClient is a small client for the subset of the RabbitMQ management
// HTTP API this engine needs: queue introspection (type, arguments,
// ready/unacked/consumer counts, exclusive owner) and forced connection
// closure, used to fence a predecessor's lock connection.
type mgmtClient struct {
	base  *url.URL // scheme + host, no userinfo
	user  string
	pass  string
	vhost string // the AMQP connection's vhost, percent-escaped for use in a path segment
	hc    *http.Client
}

// newMgmtClient builds a client from rawURL, scoped to vhost (already the
// bare vhost name, not yet percent-escaped). Basic-auth credentials embedded
// in rawURL's userinfo, if any, are extracted and sent as an Authorization
// header on every request. An unparseable rawURL is recorded and surfaces as
// an error from every method instead of panicking at construction.
func newMgmtClient(rawURL, vhost string) *mgmtClient {
	c := &mgmtClient{hc: &http.Client{Timeout: 10 * time.Second}, vhost: url.PathEscape(vhost)}
	u, err := url.Parse(rawURL)
	if err != nil {
		c.base = &url.URL{} // every request fails with a clear parse-derived error below
		return c
	}
	if u.User != nil {
		c.user = u.User.Username()
		c.pass, _ = u.User.Password()
	}
	stripped := *u
	stripped.User = nil
	stripped.Path = ""
	stripped.RawPath = ""
	stripped.RawQuery = ""
	stripped.Fragment = ""
	c.base = &stripped
	return c
}

// queueDoc is the subset of GET /api/queues/<vhost>/<name> this engine reads.
type queueDoc struct {
	Name                   string          `json:"name"`
	Type                   string          `json:"type"`
	Durable                bool            `json:"durable"`
	AutoDelete             bool            `json:"auto_delete"`
	Exclusive              bool            `json:"exclusive"`
	Arguments              map[string]any  `json:"arguments"`
	MessagesReady          int             `json:"messages_ready"`
	MessagesUnacknowledged int             `json:"messages_unacknowledged"`
	Consumers              int             `json:"consumers"`
	OwnerPidDetails        ownerPidDetails `json:"owner_pid_details"`
}

// ownerPidDetails decodes the management API's owner_pid_details field. The
// broker reports this field as an empty JSON array, not an object, whenever
// no owner pid is recorded yet -- observed in the brief window right after
// declaring a fresh exclusive queue, before the broker has attributed a
// connection to it. Name reads as "" in that case, the same as an object
// with no name.
type ownerPidDetails struct {
	Name string
}

func (o *ownerPidDetails) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		o.Name = ""
		return nil
	}
	var v struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	o.Name = v.Name
	return nil
}

// notFoundError is returned by getQueue when the broker reports 404, so
// callers can distinguish "does not exist" from a transport or auth failure.
type notFoundError struct{ what string }

func (e *notFoundError) Error() string { return e.what + " not found" }

// isNotFound reports whether err is (or wraps) a notFoundError.
func isNotFound(err error) bool {
	_, ok := err.(*notFoundError) //nolint:errorlint // sentinel constructed only by this package
	return ok
}

// queueURL builds the queue-document URL by string concatenation, not by
// assigning into url.URL.Path: c.vhost is already percent-escaped (a default
// vhost of "/" must stay literally "%2F" on the wire), and url.URL.String()
// re-escapes '%' in Path, which would otherwise turn it into "%252F" and
// make every request address a vhost that does not exist.
func (c *mgmtClient) queueURL(name string) string {
	return fmt.Sprintf("%s/api/queues/%s/%s", c.base.String(), c.vhost, url.PathEscape(name))
}

// getQueue fetches one queue's document. A 404 is reported as *notFoundError.
func (c *mgmtClient) getQueue(ctx context.Context, name string) (*queueDoc, error) {
	var doc queueDoc
	if err := c.getJSON(ctx, c.queueURL(name), &doc); err != nil {
		if isNotFound(err) {
			return nil, &notFoundError{what: fmt.Sprintf("queue %q", name)}
		}
		return nil, err
	}
	return &doc, nil
}

// queueExists reports whether name currently exists.
func (c *mgmtClient) queueExists(ctx context.Context, name string) (bool, error) {
	_, err := c.getQueue(ctx, name)
	if err == nil {
		return true, nil
	}
	if isNotFound(err) {
		return false, nil
	}
	return false, err
}

// closeConnection force-closes the named AMQP connection (the peer-address
// string the management API reports, e.g. from a queue's
// owner_pid_details.name -- not a client-provided connection_name property).
func (c *mgmtClient) closeConnection(ctx context.Context, name string) error {
	reqURL := c.base.String() + "/api/connections/" + url.PathEscape(name)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, reqURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Reason", "fenced-by-replacement-queue-agent")
	c.setAuth(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("DELETE %s: unexpected status %s", reqURL, resp.Status)
	}
	return nil
}

// waitForQueue polls getQueue(name) until cond(doc) is true or fenceDeadline
// elapses (bounded by both fenceDeadline and ctx). It returns the last
// document read and a non-nil error only on timeout, context cancellation,
// or a non-404 transport error; a queue that never appears keeps polling
// until the deadline, since 404 is a valid transient state (for example, a
// queue mid-delete). Every caller in this package needs the same generous
// bound (queue-state convergence and predecessor eviction share one
// deadline), so it is not a parameter.
func (c *mgmtClient) waitForQueue(
	ctx context.Context, name string, cond func(*queueDoc) bool,
) (*queueDoc, error) {
	ctx, cancel := context.WithTimeout(ctx, fenceDeadline)
	defer cancel()

	var last *queueDoc
	for {
		doc, err := c.getQueue(ctx, name)
		switch {
		case err == nil:
			last = doc
			if cond(doc) {
				return doc, nil
			}
		case !isNotFound(err):
			return last, err
		}
		select {
		case <-ctx.Done():
			return last, fmt.Errorf("waiting for queue %q: %w", name, ctx.Err())
		case <-time.After(mgmtPollInterval):
		}
	}
}

// getJSON performs a GET and decodes a JSON body into into. A 404 response
// is reported as *notFoundError.
func (c *mgmtClient) getJSON(ctx context.Context, rawURL string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	c.setAuth(req)
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return &notFoundError{what: rawURL}
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: unexpected status %s", rawURL, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(into)
}

func (c *mgmtClient) setAuth(req *http.Request) {
	if c.user != "" || c.pass != "" {
		req.SetBasicAuth(c.user, c.pass)
	}
}
