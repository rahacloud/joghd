// Package alerter delivers health-check alerts to notification channels.
//
// Every channel implements [Alerter]. [CompositeAlerter] fans one alert out to
// several of them and joins their failures, [CompanyFilter] restricts a
// channel to the targets of particular companies, and [TelegramAlerter] and
// [MattermostAlerter] are the shipped implementations. Payloads are encoded
// with encoding/json/v2 (see json.go).
package alerter

import (
	"context"
	"io"

	"resty.dev/v3"

	"github.com/rahacloud/joghd/internal/domain"
	"github.com/rahacloud/joghd/internal/proxy"
)

// Alerter defines the interface for sending alerts.
type Alerter interface {
	// Send sends an alert notification.
	Send(ctx context.Context, alert domain.Alert) error

	// Name returns the alerter implementation name for logging.
	Name() string
}

// Option configures the HTTP client an alerter sends through.
type Option func(*resty.Client)

// WithProxy routes the alerter's requests according to a proxy setting: a
// proxy URL, [proxy.Direct], or empty for the environment-driven default.
func WithProxy(setting string) Option {
	return func(c *resty.Client) {
		proxy.Apply(c, setting)
	}
}

// applyOptions applies opts to client and returns it, for use in
// constructors.
func applyOptions(client *resty.Client, opts []Option) *resty.Client {
	for _, opt := range opts {
		opt(client)
	}

	return client
}

// releaseBody drains and closes a response body so its connection can be
// reused. resty leaves the body open whenever it has nowhere to decode it.
func releaseBody(resp *resty.Response) {
	if resp == nil || resp.Body == nil {
		return
	}

	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}
