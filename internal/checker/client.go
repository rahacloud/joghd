package checker

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"sync"
	"time"

	"resty.dev/v3"

	"github.com/rahacloud/joghd/internal/config"
	"github.com/rahacloud/joghd/internal/proxy"
)

// maxDrainBytes caps how much of an unused response body is read back before
// the connection is released. Draining lets the connection return to the pool;
// capping it stops an oversized body from wasting bandwidth.
const maxDrainBytes = 64 << 10

// Request describes one HTTP probe.
type Request struct {
	Method  string
	URL     string
	Headers map[string]string
	// Timeout overrides the client-wide timeout when positive.
	Timeout time.Duration
	// Proxy is the probe's proxy setting: a proxy URL, [proxy.Direct], or
	// empty for the environment-driven default.
	Proxy string
}

// Response is what a probe observed. Latency is set even when the request
// failed.
type Response struct {
	StatusCode int
	Latency    time.Duration
	// CertNotAfter is the expiry of the server's leaf certificate, or zero
	// when the request did not use TLS.
	CertNotAfter time.Time
}

// HTTPClient abstracts HTTP operations for testability.
type HTTPClient interface {
	Execute(ctx context.Context, req Request) (Response, error)
}

// RestyClient wraps resty for HTTP operations. It keeps one resty client per
// distinct proxy setting, so targets that share a proxy also share a
// connection pool.
type RestyClient struct {
	cfg config.HTTPConfig

	mu      sync.Mutex
	clients map[string]*resty.Client
}

// NewRestyClient creates a new HTTP client with the given configuration.
func NewRestyClient(cfg config.HTTPConfig) *RestyClient {
	return &RestyClient{
		cfg:     cfg,
		clients: make(map[string]*resty.Client),
	}
}

// clientFor returns the resty client for a proxy setting, building it on
// first use.
func (c *RestyClient) clientFor(setting string) *resty.Client {
	c.mu.Lock()
	defer c.mu.Unlock()

	if client, ok := c.clients[setting]; ok {
		return client
	}

	client := resty.New().
		SetTimeout(c.cfg.Timeout).
		SetHeader("User-Agent", c.cfg.UserAgent)

	if c.cfg.SkipTLSVerification {
		client.SetTLSClientConfig(&tls.Config{
			//nolint:gosec // explicitly opted into via http.skip_tls_verification
			InsecureSkipVerify: true,
		})
	}

	proxy.Apply(client, setting)

	c.clients[setting] = client

	return client
}

// Execute performs an HTTP request and reports the status code and latency.
func (c *RestyClient) Execute(ctx context.Context, req Request) (Response, error) {
	r := c.clientFor(req.Proxy).R().SetContext(ctx)

	// Override the client-wide timeout for this request only. Building a
	// separate client per request would drop the TLS and header settings
	// configured above and leak its connection pool.
	if req.Timeout > 0 {
		r.SetTimeout(req.Timeout)
	}

	for k, v := range req.Headers {
		r.SetHeader(k, v)
	}

	start := time.Now()
	resp, err := r.Execute(req.Method, req.URL)
	out := Response{Latency: time.Since(start)}

	// Only the status code is of interest, and resty leaves the body open
	// when it has nowhere to decode it, so release it here.
	releaseBody(resp)

	if err != nil {
		return out, err
	}

	out.StatusCode = resp.StatusCode()

	// The connection state of the verified handshake: no second connection,
	// and no reason to skip verification to read the certificate.
	if raw := resp.RawResponse; raw != nil && raw.TLS != nil && len(raw.TLS.PeerCertificates) > 0 {
		out.CertNotAfter = raw.TLS.PeerCertificates[0].NotAfter
	}

	return out, nil
}

// Close releases the idle connections of every client built so far.
func (c *RestyClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	var errs []error
	for _, client := range c.clients {
		errs = append(errs, client.Close())
	}

	return errors.Join(errs...)
}

// releaseBody drains and closes a response body so its connection can be
// reused instead of being held open for the lifetime of the process.
func releaseBody(resp *resty.Response) {
	if resp == nil || resp.Body == nil {
		return
	}

	_, _ = io.CopyN(io.Discard, resp.Body, maxDrainBytes)
	_ = resp.Body.Close()
}
