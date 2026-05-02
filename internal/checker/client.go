package checker

import (
	"context"
	"crypto/tls"
	"time"

	"github.com/rahacloud/joghd/internal/config"
	"resty.dev/v3"
)

// HTTPClient abstracts HTTP operations for testability.
type HTTPClient interface {
	Execute(ctx context.Context, method, url string, headers map[string]string, timeout time.Duration) (statusCode int, latency time.Duration, err error)
}

// RestyClient wraps resty for HTTP operations.
type RestyClient struct {
	client *resty.Client
}

// NewRestyClient creates a new HTTP client with the given configuration.
func NewRestyClient(cfg config.HTTPConfig) *RestyClient {
	client := resty.New().
		SetTimeout(cfg.Timeout).
		SetHeader("User-Agent", cfg.UserAgent)

	if cfg.SkipTLSVerification {
		client.SetTLSClientConfig(&tls.Config{
			InsecureSkipVerify: true,
		})
	}

	return &RestyClient{client: client}
}

// Execute performs an HTTP request and returns the status code, latency, and any error.
func (c *RestyClient) Execute(ctx context.Context, method, url string, headers map[string]string, timeout time.Duration) (int, time.Duration, error) {
	req := c.client.R().SetContext(ctx)

	if timeout > 0 {
		// Create a new client with specific timeout for this request
		reqClient := resty.New().SetTimeout(timeout)
		req = reqClient.R().SetContext(ctx)

		// Copy headers from parent client
		req.SetHeader("User-Agent", c.client.Header().Get("User-Agent"))
	}

	for k, v := range headers {
		req.SetHeader(k, v)
	}

	start := time.Now()
	resp, err := req.Execute(method, url)
	latency := time.Since(start)

	if err != nil {
		return 0, latency, err
	}

	return resp.StatusCode(), latency, nil
}
