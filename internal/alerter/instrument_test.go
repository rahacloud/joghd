package alerter

import (
	"errors"
	"testing"

	"github.com/rahacloud/joghd/internal/domain"
)

type recordingObserver struct {
	alerters []string
	errs     []error
}

func (o *recordingObserver) ObserveAlert(alerter string, _ domain.Alert, err error) {
	o.alerters = append(o.alerters, alerter)
	o.errs = append(o.errs, err)
}

func TestInstrumentedReportsEachDelivery(t *testing.T) {
	down := errors.New("telegram is down")
	inner := &stubAlerter{name: "rahacloud", err: down}
	obs := &recordingObserver{}

	err := NewInstrumented(inner, obs).Send(t.Context(), alertFor("raha"))
	if !errors.Is(err, down) {
		t.Fatalf("Send error = %v, want %v", err, down)
	}

	if len(obs.alerters) != 1 || obs.alerters[0] != "rahacloud" || !errors.Is(obs.errs[0], down) {
		t.Fatalf("observed %v / %v, want one failed delivery by rahacloud", obs.alerters, obs.errs)
	}
}

func TestInstrumentedInsideFilterSkipsDroppedAlerts(t *testing.T) {
	obs := &recordingObserver{}
	filtered := NewCompanyFilter(NewInstrumented(&stubAlerter{name: "acme"}, obs), []string{"acme"})

	if err := filtered.Send(t.Context(), alertFor("raha")); err != nil {
		t.Fatalf("Send: %v", err)
	}

	if len(obs.alerters) != 0 {
		t.Fatalf("observed %v, want nothing for an alert the filter dropped", obs.alerters)
	}
}
