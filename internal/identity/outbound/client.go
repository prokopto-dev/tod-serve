package outbound

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DefaultTimeout bounds one outbound request end to end. A provider that hangs must not hold a
// join request open: the caller's own ctx still applies and is usually shorter, but a background
// JWKS refresh has no request behind it and would otherwise wait forever.
const DefaultTimeout = 10 * time.Second

// DefaultMaxResponseBytes caps a response body. A JWKS document is a few kilobytes and a Discord
// user object is smaller; a megabyte is three orders of magnitude of headroom and still small
// enough that a hostile endpoint streaming zeroes cannot exhaust a home server's memory.
const DefaultMaxResponseBytes int64 = 1 << 20

// ErrTooLarge is returned when a response body exceeds the cap. It unwraps to [ErrRefused].
var ErrTooLarge = fmt.Errorf("response exceeds the size cap: %w", ErrRefused)

// ErrRedirect is returned when a response is a redirect. Redirects are not followed: following
// one hands the choice of destination to the endpoint, which is the entire thing the host
// allowlist and the deny list exist to keep. It unwraps to [ErrRefused].
var ErrRedirect = fmt.Errorf("redirect refused; this client follows none: %w", ErrRefused)

// ErrHostNotAllowed is returned for a host outside the caller's allowlist. It unwraps to
// [ErrRefused].
var ErrHostNotAllowed = fmt.Errorf("host is not on this client's allowlist: %w", ErrRefused)

// Policy is what a caller must decide before it can have a client. Every field is required:
// a zero-valued policy is an error rather than a permissive default, because the permissive
// default is exactly the client this package exists to make unavailable.
type Policy struct {
	// AllowHosts is the exact set of hostnames this client may reach. Matching is on the host
	// alone, case-folded, with no wildcards and no suffix matching: `discord.com` does not admit
	// `evil-discord.com` and does not admit `cdn.discord.com` either. A caller that needs a
	// second host names it.
	AllowHosts []string

	// Timeout bounds one request. Zero means [DefaultTimeout].
	Timeout time.Duration

	// MaxResponseBytes caps a response body. Zero means [DefaultMaxResponseBytes].
	MaxResponseBytes int64
}

// hostSet is the allowlist, resolved once at construction.
type hostSet map[string]struct{}

func (h hostSet) check(host string) error {
	if _, ok := h[strings.ToLower(host)]; !ok {
		return fmt.Errorf("%s: %w", host, ErrHostNotAllowed)
	}
	return nil
}

// Client issues guarded outbound requests. It is safe for concurrent use.
type Client struct {
	http     *http.Client
	allow    hostSet
	maxBytes int64
}

// Response is a completed exchange with the body already read and closed.
//
// This is deliberately not an *http.Response. Handing one back would put a body-closing
// obligation on every caller in every provider — the leak `bodyclose` is enabled to catch — and
// would let a caller read past the size cap by reaching for the underlying reader. The cap is
// only a cap if nothing can route around it.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// New returns a client bound to p. It fails rather than defaulting when the allowlist is empty:
// a client that may reach any host is the one thing this package must not be able to produce.
func New(p Policy) (*Client, error) {
	if len(p.AllowHosts) == 0 {
		return nil, errors.New("outbound policy names no allowed hosts")
	}
	allow := make(hostSet, len(p.AllowHosts))
	for _, h := range p.AllowHosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if h == "" {
			return nil, errors.New("outbound policy names an empty host")
		}
		allow[h] = struct{}{}
	}

	timeout := p.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	maxBytes := p.MaxResponseBytes
	if maxBytes == 0 {
		maxBytes = DefaultMaxResponseBytes
	}

	dialer := &guardedDialer{
		resolve: net.DefaultResolver,
		dial:    (&net.Dialer{Timeout: timeout, Control: control}).DialContext,
		allow:   allow,
	}

	transport := &http.Transport{
		DialContext: dialer.DialContext,
		// DialContext is used for the TLS path too, because the guard has to run before the
		// handshake. http.Transport builds the TLS connection over whatever DialContext returns
		// as long as DialTLSContext is nil, which is why that field is deliberately unset.
		Proxy: nil, // A proxy would dial on our behalf, which is the guard removed.
		// A connection pooled from an earlier request is a connection whose address was checked
		// when it was made. That is still true — the address cannot change under an open socket —
		// so pooling is kept, bounded so a provider cannot pin many file descriptors open.
		MaxIdleConns:          8,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		ForceAttemptHTTP2:     true,
	}

	return &Client{
		http: &http.Client{
			Transport: transport,
			Timeout:   timeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return ErrRedirect
			},
		},
		allow:    allow,
		maxBytes: maxBytes,
	}, nil
}

// Do issues one request and returns the response with its body read and closed.
//
// ctx is the caller's, always — `noctx` is enabled for exactly this reason. A join request that
// the user abandoned must not leave a socket open to Discord.
func (c *Client) Do(ctx context.Context, method, rawURL string, header http.Header, body []byte) (*Response, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse url: %w", err)
	}
	if u.Scheme != "https" {
		return nil, fmt.Errorf("%s is %s, not https: %w", rawURL, u.Scheme, ErrRefused)
	}
	if err := c.allow.check(u.Hostname()); err != nil {
		return nil, err
	}

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, fmt.Errorf("build request %s %s: %w", method, u.Redacted(), err)
	}
	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request %s %s: %w", method, u.Redacted(), err)
	}
	defer closeBody(resp)

	// One byte past the cap, so a body that is exactly at the limit is accepted and one that is
	// over it is detected rather than silently truncated. A truncated JSON document parses as a
	// syntax error somewhere unrelated, which is a bug report about the wrong thing.
	read, err := io.ReadAll(io.LimitReader(resp.Body, c.maxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read response %s %s: %w", method, u.Redacted(), err)
	}
	if int64(len(read)) > c.maxBytes {
		return nil, fmt.Errorf("%s %s: %w", method, u.Redacted(), ErrTooLarge)
	}

	return &Response{Status: resp.StatusCode, Header: resp.Header, Body: read}, nil
}

// closeBody discards the close error deliberately, and this is the one place that says why: the
// body has already been read to completion or to the cap, so anything Close reports is either a
// repeat of an error ReadAll already returned or a connection-teardown detail with no caller that
// could act on it. Returning it would displace the error that matters.
func closeBody(resp *http.Response) { _ = resp.Body.Close() }

// Doer is the guarded client, as the provider packages see it.
//
// It is an interface for one reason: a provider test must be able to answer a request without a
// socket, and the alternative — pointing the provider at an httptest server — would mean the
// server was on 127.0.0.1 and the guard refused it, which is the guard working correctly and the
// test failing for the wrong reason. The guard is tested directly instead, against a real socket.
// In the running binary the only implementation is [*Client].
type Doer interface {
	Do(ctx context.Context, method, url string, header http.Header, body []byte) (*Response, error)
}

// The compiler holds the guarded client to the interface its callers take.
var _ Doer = (*Client)(nil)
