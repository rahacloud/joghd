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

const (
	StatusHealthy HealthStatus = iota
	StatusUnhealthy
	StatusUnknown
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
