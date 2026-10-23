package metrics

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/rahacloud/joghd/internal/domain"
)

func httpsTarget() domain.Target {
	return domain.Target{Name: "api", URL: "https://api.example.com/health", Company: "raha"}
}

func TestObserveCheckSetsTheTargetSeries(t *testing.T) {
	m := New("1.5.0", "abc")
	tgt := httpsTarget()
	m.RegisterTarget(tgt)

	notAfter := time.Unix(1_800_000_000, 0)
	m.ObserveCheck(domain.CheckResult{
		Target: tgt, Success: true, ActualStatus: 200, Attempts: 1,
		Latency: 250 * time.Millisecond, Timestamp: time.Unix(1_700_000_000, 0), CertNotAfter: notAfter,
	})

	labels := []string{"api", "raha", "https"}
	for name, tc := range map[string]struct {
		got, want float64
	}{
		"up":          {testutil.ToFloat64(m.up.WithLabelValues(labels...)), 1},
		"latency":     {testutil.ToFloat64(m.latency.WithLabelValues(labels...)), 0.25},
		"status":      {testutil.ToFloat64(m.status.WithLabelValues(labels...)), 200},
		"last check":  {testutil.ToFloat64(m.lastCheck.WithLabelValues(labels...)), 1_700_000_000},
		"cert expiry": {testutil.ToFloat64(m.certExpiry.WithLabelValues(labels...)), 1_800_000_000},
		"successes":   {testutil.ToFloat64(m.checks.WithLabelValues("api", "raha", "https", resultSuccess)), 1},
		"failures":    {testutil.ToFloat64(m.checks.WithLabelValues("api", "raha", "https", resultFailure)), 0},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", name, tc.got, tc.want)
		}
	}

	// A failed check without a certificate keeps the last expiry seen.
	m.ObserveCheck(domain.CheckResult{Target: tgt, Error: errors.New("connection refused")})

	if got := testutil.ToFloat64(m.up.WithLabelValues(labels...)); got != 0 {
		t.Errorf("up after a failure = %v, want 0", got)
	}
	if got := testutil.ToFloat64(m.certExpiry.WithLabelValues(labels...)); got != 1_800_000_000 {
		t.Errorf("cert expiry after a failure = %v, want it kept", got)
	}
}

func TestObserveAlertCountsByOutcome(t *testing.T) {
	m := New("1.5.0", "abc")
	m.RegisterAlerter("rahacloud")

	alert := domain.NewFailureAlert(domain.CheckResult{Target: httpsTarget()})
	m.ObserveAlert("rahacloud", alert, nil)
	m.ObserveAlert("rahacloud", alert, errors.New("telegram is down"))
	m.ObserveAlert("rahacloud", alert, errors.New("telegram is down"))

	if got := testutil.ToFloat64(m.alerts.WithLabelValues("rahacloud", "failure")); got != 1 {
		t.Errorf("sent = %v, want 1", got)
	}
	if got := testutil.ToFloat64(m.alertFailures.WithLabelValues("rahacloud", "failure")); got != 2 {
		t.Errorf("failures = %v, want 2", got)
	}
	if got := testutil.ToFloat64(m.alertFailures.WithLabelValues("rahacloud", "recovery")); got != 0 {
		t.Errorf("recovery failures = %v, want a registered 0", got)
	}
}

func TestHandlerServesTheRegistry(t *testing.T) {
	m := New("1.5.0", "abc")
	m.RegisterTarget(domain.Target{Name: "db", URL: "tcp://db.example.com:5432", Company: "raha"})

	srv := httptest.NewServer(m.Handler())
	defer srv.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL, nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{
		`joghd_build_info{commit="abc",version="1.5.0"} 1`,
		`joghd_checks_total{company="raha",result="failure",scheme="tcp",target="db"} 0`,
		`go_goroutines`,
	} {
		if !strings.Contains(string(body), want) {
			t.Errorf("/metrics is missing %q", want)
		}
	}
}
