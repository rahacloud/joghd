// Package domain holds the core joghd types shared by every other package:
// the [Target] being monitored, the [CheckResult] of probing it, and the
// [Alert] raised when its health changes.
package domain

import "time"

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
