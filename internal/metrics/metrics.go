// Package metrics exposes joghd's view of its targets as Prometheus metrics.
//
// [Metrics] keeps one series per target for its latest check (up, latency,
// status code, certificate expiry) plus counters for checks and alert
// deliveries, on a registry of its own so nothing but joghd's metrics and the
// Go runtime's ends up on /metrics. [Nop] is the [Recorder] used when no
// metrics endpoint is configured.
package metrics

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/rahacloud/joghd/internal/domain"
)

const namespace = "joghd"

// Results of a check, as the result label of joghd_checks_total.
const (
	resultSuccess = "success"
	resultFailure = "failure"
)

// targetLabels identify a target on every per-target series. scheme is http,
// https or tcp, so a dashboard can tell the kinds of check apart.
var targetLabels = []string{"target", "company", "scheme"}

// Recorder receives everything the scheduler and the alerters observe.
type Recorder interface {
	// RegisterTarget creates a target's counters at zero before its first
	// check, so a rule over joghd_checks_total sees zero rather than nothing.
	RegisterTarget(target domain.Target)

	// RegisterAlerter does the same for an alerter's delivery counters, so
	// increase(joghd_alert_failures_total[...]) works before any failure.
	RegisterAlerter(name string)

	// ObserveCheck records the outcome of one (retried) check.
	ObserveCheck(result domain.CheckResult)

	// ObserveAlert records one delivery attempt of alert by the named
	// alerter; err is nil when it was delivered.
	ObserveAlert(alerter string, alert domain.Alert, err error)
}

// Nop is a Recorder that records nothing.
type Nop struct{}

func (Nop) RegisterTarget(domain.Target)             {}
func (Nop) RegisterAlerter(string)                   {}
func (Nop) ObserveCheck(domain.CheckResult)          {}
func (Nop) ObserveAlert(string, domain.Alert, error) {}

// Metrics is the Prometheus-backed Recorder.
type Metrics struct {
	registry *prometheus.Registry

	up            *prometheus.GaugeVec
	latency       *prometheus.GaugeVec
	status        *prometheus.GaugeVec
	attempts      *prometheus.GaugeVec
	lastCheck     *prometheus.GaugeVec
	certExpiry    *prometheus.GaugeVec
	checks        *prometheus.CounterVec
	alerts        *prometheus.CounterVec
	alertFailures *prometheus.CounterVec
}

// New builds the metrics and registers them, together with the Go runtime and
// process collectors and joghd_build_info, on a fresh registry.
func New(version, commit string) *Metrics {
	gauge := func(name, help string) *prometheus.GaugeVec {
		return prometheus.NewGaugeVec(prometheus.GaugeOpts{Namespace: namespace, Name: name, Help: help}, targetLabels)
	}

	m := &Metrics{
		registry: prometheus.NewRegistry(),

		up:         gauge("target_up", "1 if the target's latest check passed, 0 if it failed."),
		latency:    gauge("target_latency_seconds", "Latency of the latest check's final attempt."),
		status:     gauge("target_status_code", "HTTP status of the latest check, 0 for a TCP check or no response."),
		attempts:   gauge("target_attempts", "Attempts the latest check took, retries included."),
		lastCheck:  gauge("target_last_check_timestamp_seconds", "Unix time of the latest check."),
		certExpiry: gauge("target_cert_expiry_timestamp_seconds", "Unix time the target's TLS certificate expires, for https targets."),

		checks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "checks_total", Help: "Checks run, by result.",
		}, append(targetLabels, "result")),
		alerts: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "alerts_sent_total", Help: "Alerts delivered, by alerter and kind.",
		}, []string{"alerter", "kind"}),
		alertFailures: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "alert_failures_total", Help: "Alerts an alerter failed to deliver, by alerter and kind.",
		}, []string{"alerter", "kind"}),
	}

	buildInfo := prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: namespace, Name: "build_info", Help: "Always 1; the labels carry joghd's version and commit.",
	}, []string{"version", "commit"})
	buildInfo.WithLabelValues(version, commit).Set(1)

	m.registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		buildInfo,
		m.up, m.latency, m.status, m.attempts, m.lastCheck, m.certExpiry,
		m.checks, m.alerts, m.alertFailures,
	)

	return m
}

// Handler serves the registry in the Prometheus exposition format.
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{Registry: m.registry})
}

func (m *Metrics) RegisterTarget(target domain.Target) {
	labels := labelsFor(target)

	m.checks.WithLabelValues(append(labels, resultSuccess)...)
	m.checks.WithLabelValues(append(labels, resultFailure)...)
}

func (m *Metrics) RegisterAlerter(name string) {
	for _, t := range []domain.AlertType{domain.AlertTypeFailure, domain.AlertTypeReminder, domain.AlertTypeRecovery} {
		m.alerts.WithLabelValues(name, kindOf(t))
		m.alertFailures.WithLabelValues(name, kindOf(t))
	}
}

func (m *Metrics) ObserveCheck(result domain.CheckResult) {
	labels := labelsFor(result.Target)

	up, outcome := 0.0, resultFailure
	if result.Success {
		up, outcome = 1, resultSuccess
	}

	m.up.WithLabelValues(labels...).Set(up)
	m.latency.WithLabelValues(labels...).Set(result.Latency.Seconds())
	m.status.WithLabelValues(labels...).Set(float64(result.ActualStatus))
	m.attempts.WithLabelValues(labels...).Set(float64(result.Attempts))
	m.lastCheck.WithLabelValues(labels...).Set(float64(result.Timestamp.Unix()))
	m.checks.WithLabelValues(append(labels, outcome)...).Inc()

	// A failed handshake reports no certificate; keep the last one seen
	// rather than dropping the series for the length of an outage.
	if !result.CertNotAfter.IsZero() {
		m.certExpiry.WithLabelValues(labels...).Set(float64(result.CertNotAfter.Unix()))
	}
}

func (m *Metrics) ObserveAlert(alerter string, alert domain.Alert, err error) {
	if err != nil {
		m.alertFailures.WithLabelValues(alerter, kindOf(alert.Type)).Inc()

		return
	}

	m.alerts.WithLabelValues(alerter, kindOf(alert.Type)).Inc()
}

// kindOf is the alert type as a label value: failure, reminder or recovery.
func kindOf(t domain.AlertType) string {
	return strings.ToLower(t.String())
}

func labelsFor(target domain.Target) []string {
	scheme := ""
	if u, err := url.Parse(target.URL); err == nil {
		scheme = u.Scheme
	}

	return []string{target.Name, target.Company, scheme}
}
