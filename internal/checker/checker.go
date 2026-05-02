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
		retryConfig: config.RetryConfig{
			MaxAttempts: 3,
			InitialWait: time.Second,
			MaxWait:     10 * time.Second,
			Multiplier:  2.0,
		},
		concurrency: 10,
	}

	for _, opt := range opts {
		opt(c)
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

		statusCode, latency, err := c.httpClient.Execute(
			ctx,
			target.Method,
			target.URL,
			target.Headers,
			target.Timeout,
		)

		result.Attempts = attempt
		result.Latency = latency
		result.ActualStatus = statusCode

		if err == nil && statusCode == target.ExpectedStatus {
			result.Success = true
			return result
		}

		if err != nil {
			result.Error = err
		} else {
			result.Error = fmt.Errorf("status mismatch: expected %d, got %d", target.ExpectedStatus, statusCode)
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
