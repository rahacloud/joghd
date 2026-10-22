// Package domain holds the core joghd types shared by every other package:
// the [Target] being monitored, the [CheckResult] of probing it, and the
// [Alert] raised when its health changes.
package domain

import (
	"net/url"
	"time"
)

// SchemeTCP is the URL scheme of a TCP connect check: a target whose url is
// tcp://host:port passes when a TCP connection to host:port is established.
const SchemeTCP = "tcp"

// Target represents a URL endpoint to be health-checked.
type Target struct {
	Name           string            `koanf:"name"`
	URL            string            `koanf:"url"`
	ExpectedStatus int               `koanf:"expected_status"`
	Method         string            `koanf:"method"`
	Timeout        time.Duration     `koanf:"timeout"`
	Interval       time.Duration     `koanf:"interval"`
	Headers        map[string]string `koanf:"headers"`
	Company        string            `koanf:"company"`
	Contact        string            `koanf:"contact"`

	// Proxy routes this target's checks through a proxy: a proxy URL, the
	// literal "direct" for none at all, or empty to inherit http.proxy.
	Proxy string `koanf:"proxy"`
}

// IsTCP reports whether the target is a TCP connect check rather than an HTTP
// request.
func (t Target) IsTCP() bool {
	u, err := url.Parse(t.URL)

	return err == nil && u.Scheme == SchemeTCP
}

// TCPAddress returns the host:port a TCP connect check dials, or "" for an
// HTTP target.
func (t Target) TCPAddress() string {
	u, err := url.Parse(t.URL)
	if err != nil || u.Scheme != SchemeTCP {
		return ""
	}

	return u.Host
}

// CheckResult represents the outcome of a health check.
type CheckResult struct {
	Target       Target
	Success      bool
	ActualStatus int
	Error        error
	Latency      time.Duration
	Timestamp    time.Time
	Attempts     int
}

// HealthStatus represents the overall health state of a target.
type HealthStatus int

// StatusUnknown is first so that the zero value of HealthStatus means "not
// checked yet" rather than "healthy".
const (
	StatusUnknown HealthStatus = iota
	StatusHealthy
	StatusUnhealthy
)

func (s HealthStatus) String() string {
	switch s {
	case StatusHealthy:
		return "healthy"
	case StatusUnhealthy:
		return "unhealthy"
	default:
		return "unknown"
	}
}
