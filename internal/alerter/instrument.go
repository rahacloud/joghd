package alerter

import (
	"context"

	"github.com/rahacloud/joghd/internal/domain"
)

// Observer is told about every delivery attempt an [Instrumented] alerter
// makes. metrics.Recorder satisfies it.
type Observer interface {
	ObserveAlert(alerter string, alert domain.Alert, err error)
}

// Instrumented reports each Send of the wrapped alerter to an Observer. Wrap
// it inside a [CompanyFilter], so alerts the filter drops are not counted.
type Instrumented struct {
	inner    Alerter
	observer Observer
}

// NewInstrumented wraps inner so that obs sees each delivery attempt.
func NewInstrumented(inner Alerter, obs Observer) *Instrumented {
	return &Instrumented{inner: inner, observer: obs}
}

// Send forwards the alert and reports the outcome.
func (i *Instrumented) Send(ctx context.Context, alert domain.Alert) error {
	err := i.inner.Send(ctx, alert)
	i.observer.ObserveAlert(i.inner.Name(), alert, err)

	return err
}

// Name returns the wrapped alerter's name.
func (i *Instrumented) Name() string {
	return i.inner.Name()
}
