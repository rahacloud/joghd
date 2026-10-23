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
	"errors"
	"fmt"
	"net/url"
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

		resp, err := c.probe(ctx, target)

		result.Attempts = attempt
		result.Latency = resp.Latency
		result.ActualStatus = resp.StatusCode
		result.CertNotAfter = resp.CertNotAfter

		if err == nil {
			result.Success = true
			// Discard any error recorded by an earlier attempt so a
			// successful result never carries a stale failure.
			result.Error = nil

			return result
		}

		result.Error = err

		// A certificate does not renew itself between attempts; retrying
		// only delays the alert.
		if _, expiring := errors.AsType[*CertExpiryError](err); expiring {
			return result
		}

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

// probe runs one attempt against target and returns what it observed (a zero
// status for a TCP check) and nil only if the attempt passed.
func (c *checker) probe(ctx context.Context, target domain.Target) (Response, error) {
	if target.IsTCP() {
		latency, err := c.tcpDialer.Dial(ctx, target.TCPAddress(), target.Proxy, target.Timeout)

		return Response{Latency: latency}, err
	}

	resp, err := c.httpClient.Execute(ctx, Request{
		Method:  target.Method,
		URL:     target.URL,
		Headers: target.Headers,
		Timeout: target.Timeout,
		Proxy:   target.Proxy,
	})
	if err != nil {
		return resp, err
	}

	if resp.StatusCode != target.ExpectedStatus {
		return resp, fmt.Errorf("status mismatch: expected %d, got %d", target.ExpectedStatus, resp.StatusCode)
	}

	if window := target.CertExpiryWarning; window > 0 && !resp.CertNotAfter.IsZero() {
		if time.Until(resp.CertNotAfter) < window {
			host := target.URL
			if u, err := url.Parse(target.URL); err == nil {
				host = u.Hostname()
			}

			return resp, &CertExpiryError{Host: host, NotAfter: resp.CertNotAfter}
		}
	}

	return resp, nil
}

// CertExpiryError fails an https check whose certificate expires within the
// target's cert_expiry_warning window.
type CertExpiryError struct {
	Host     string
	NotAfter time.Time
}

// Error says how long the certificate has left, e.g. "certificate for
// example.com expires in 9 days (2026-11-03)".
func (e *CertExpiryError) Error() string {
	date := e.NotAfter.UTC().Format(time.DateOnly)
	left := time.Until(e.NotAfter)

	switch days := int(left.Hours() / 24); {
	case left <= 0:
		return fmt.Sprintf("certificate for %s expired on %s", e.Host, date)
	case days == 0:
		return fmt.Sprintf("certificate for %s expires in less than a day (%s)", e.Host, date)
	case days == 1:
		return fmt.Sprintf("certificate for %s expires in 1 day (%s)", e.Host, date)
	default:
		return fmt.Sprintf("certificate for %s expires in %d days (%s)", e.Host, days, date)
	}
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
