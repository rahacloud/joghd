// Package checker performs the health checks themselves.
//
// [Checker] probes one target or a whole set of them, retrying with capped
// exponential backoff and bounding how many probes run at once. An http or
// https target is an HTTP request through [HTTPClient] ([RestyClient] in
// production); a tcp://host:port target is a TCP connect through [TCPDialer]
// ([NetDialer] in production). Both are interfaces so tests can substitute
// them.
package checker

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/rahacloud/joghd/internal/config"
	"github.com/rahacloud/joghd/internal/domain"
)

// Checker defines the interface for health checking.
type Checker interface {
	Check(ctx context.Context, target domain.Target) domain.CheckResult
	CheckAll(ctx context.Context, targets []domain.Target) []domain.CheckResult
}

// checker implements the Checker interface.
type checker struct {
	httpClient  HTTPClient
	tcpDialer   TCPDialer
	retryConfig config.RetryConfig
	concurrency int
}

// Option is a functional option for configuring the checker.
type Option func(*checker)

// WithHTTPClient sets a custom HTTP client.
func WithHTTPClient(client HTTPClient) Option {
	return func(c *checker) {
		c.httpClient = client
	}
}

// WithTCPDialer sets a custom dialer for tcp:// targets.
func WithTCPDialer(dialer TCPDialer) Option {
	return func(c *checker) {
		c.tcpDialer = dialer
	}
}

// WithRetryConfig sets the retry configuration.
func WithRetryConfig(cfg config.RetryConfig) Option {
	return func(c *checker) {
		c.retryConfig = cfg
	}
}

// WithConcurrency sets the maximum concurrent checks.
func WithConcurrency(n int) Option {
	return func(c *checker) {
		c.concurrency = n
	}
}

// New creates a new Checker with the given options.
func New(opts ...Option) Checker {
	c := &checker{
		tcpDialer:   NetDialer{},
		retryConfig: config.DefaultRetry(),
		concurrency: config.DefaultConcurrency,
	}

	for _, opt := range opts {
		opt(c)
	}

	// Guard against a caller-supplied zero: an unbuffered semaphore would
	// block every check forever. Configuration rejects this too, so reaching
	// here means the checker was constructed directly.
	if c.concurrency < 1 {
		c.concurrency = config.DefaultConcurrency
	}

	if c.retryConfig.MaxAttempts < 1 {
		c.retryConfig.MaxAttempts = 1
	}

	return c
}

// Check performs a health check on a single target with retry logic.
func (c *checker) Check(ctx context.Context, target domain.Target) domain.CheckResult {
	result := domain.CheckResult{
		Target:    target,
		Timestamp: time.Now(),
	}

	wait := c.retryConfig.InitialWait

	for attempt := 1; attempt <= c.retryConfig.MaxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			result.Error = ctx.Err()
			result.Attempts = attempt
			return result
		default:
		}

		statusCode, latency, err := c.probe(ctx, target)

		result.Attempts = attempt
		result.Latency = latency
		result.ActualStatus = statusCode

		if err == nil {
			result.Success = true
			// Discard any error recorded by an earlier attempt so a
			// successful result never carries a stale failure.
			result.Error = nil

			return result
		}

		result.Error = err

		// Don't wait after the last attempt
		if attempt < c.retryConfig.MaxAttempts {
			select {
			case <-ctx.Done():
				result.Error = ctx.Err()
				return result
			case <-time.After(wait):
			}

			// Exponential backoff
			wait = min(time.Duration(float64(wait)*c.retryConfig.Multiplier), c.retryConfig.MaxWait)
		}
	}

	return result
}

// probe runs one attempt against target and returns the HTTP status (zero for
// a TCP check), the latency, and nil only if the attempt passed.
func (c *checker) probe(ctx context.Context, target domain.Target) (int, time.Duration, error) {
	if target.IsTCP() {
		latency, err := c.tcpDialer.Dial(ctx, target.TCPAddress(), target.Proxy, target.Timeout)

		return 0, latency, err
	}

	resp, err := c.httpClient.Execute(ctx, Request{
		Method:  target.Method,
		URL:     target.URL,
		Headers: target.Headers,
		Timeout: target.Timeout,
		Proxy:   target.Proxy,
	})
	if err != nil {
		return resp.StatusCode, resp.Latency, err
	}

	if resp.StatusCode != target.ExpectedStatus {
		return resp.StatusCode, resp.Latency, fmt.Errorf("status mismatch: expected %d, got %d", target.ExpectedStatus, resp.StatusCode)
	}

	return resp.StatusCode, resp.Latency, nil
}

// CheckAll performs health checks on multiple targets concurrently.
func (c *checker) CheckAll(ctx context.Context, targets []domain.Target) []domain.CheckResult {
	results := make([]domain.CheckResult, len(targets))

	// Use semaphore to limit concurrency
	sem := make(chan struct{}, c.concurrency)
	var wg sync.WaitGroup

	for i, target := range targets {
		wg.Go(func() {

			// Acquire semaphore
			select {
			case sem <- struct{}{}:
				defer func() { <-sem }()
			case <-ctx.Done():
				results[i] = domain.CheckResult{
					Target:    target,
					Error:     ctx.Err(),
					Timestamp: time.Now(),
				}
				return
			}

			results[i] = c.Check(ctx, target)
		})
	}

	wg.Wait()
	return results
}
