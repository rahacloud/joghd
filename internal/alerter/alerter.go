package alerter

import (
	"context"

	"github.com/rahacloud/joghd/internal/domain"
)

// Alerter defines the interface for sending alerts.
type Alerter interface {
	// Send sends an alert notification.
	Send(ctx context.Context, alert domain.Alert) error

	// Name returns the alerter implementation name for logging.
	Name() string
}
