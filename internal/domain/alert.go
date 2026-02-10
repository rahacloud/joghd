package domain

import "time"

// AlertType indicates whether this is a failure or recovery alert.
type AlertType int

const (
	AlertTypeFailure AlertType = iota
	AlertTypeRecovery
	AlertTypeReminder
)

func (t AlertType) String() string {
	switch t {
	case AlertTypeFailure:
		return "FAILURE"
	case AlertTypeRecovery:
		return "RECOVERY"
	case AlertTypeReminder:
		return "REMINDER"
	default:
		return "UNKNOWN"
	}
}

// Severity level for alerts.
type Severity int

const (
	SeverityInfo Severity = iota
	SeverityWarning
	SeverityCritical
)

func (s Severity) String() string {
	switch s {
	case SeverityInfo:
		return "INFO"
	case SeverityWarning:
		return "WARNING"
	case SeverityCritical:
		return "CRITICAL"
	default:
		return "UNKNOWN"
	}
}

// Alert represents a notification to be sent.
type Alert struct {
	Type      AlertType
	Target    Target
	Result    CheckResult
	Message   string
	Severity  Severity
	Timestamp time.Time
}

// NewFailureAlert creates an alert for a failed health check.
func NewFailureAlert(result CheckResult) Alert {
	msg := "Health check failed"
	if result.Error != nil {
		msg = result.Error.Error()
	}

	return Alert{
		Type:      AlertTypeFailure,
		Target:    result.Target,
		Result:    result,
		Message:   msg,
		Severity:  SeverityCritical,
		Timestamp: time.Now(),
	}
}

// NewReminderAlert creates a reminder alert for a target that is still down.
func NewReminderAlert(result CheckResult) Alert {
	msg := "Health check still failing"
	if result.Error != nil {
		msg = result.Error.Error()
	}

	return Alert{
		Type:      AlertTypeReminder,
		Target:    result.Target,
		Result:    result,
		Message:   msg,
		Severity:  SeverityWarning,
		Timestamp: time.Now(),
	}
}

// NewRecoveryAlert creates an alert for a recovered target.
func NewRecoveryAlert(result CheckResult) Alert {
	return Alert{
		Type:      AlertTypeRecovery,
		Target:    result.Target,
		Result:    result,
		Message:   "Health check recovered",
		Severity:  SeverityInfo,
		Timestamp: time.Now(),
	}
}
