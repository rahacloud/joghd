package alerter

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/rahacloud/joghd/internal/domain"
)

// CompositeAlerter fans out alerts to multiple alerters.
type CompositeAlerter struct {
	alerters []Alerter
}

// NewCompositeAlerter creates a composite alerter from multiple alerters.
func NewCompositeAlerter(alerters ...Alerter) *CompositeAlerter {
	return &CompositeAlerter{alerters: alerters}
}

// Send sends an alert to all configured alerters and aggregates errors.
func (c *CompositeAlerter) Send(ctx context.Context, alert domain.Alert) error {
	var errs []error

	for _, alerter := range c.alerters {
		if err := alerter.Send(ctx, alert); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", alerter.Name(), err))
		}
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}

	return nil
}

// Name returns the composite alerter name.
func (c *CompositeAlerter) Name() string {
	names := make([]string, len(c.alerters))
	for i, a := range c.alerters {
		names[i] = a.Name()
	}
	return fmt.Sprintf("composite[%s]", strings.Join(names, ","))
}

// Add adds an alerter to the composite.
func (c *CompositeAlerter) Add(alerter Alerter) {
	c.alerters = append(c.alerters, alerter)
}
