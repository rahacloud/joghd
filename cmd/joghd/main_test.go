package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/fx"
	"go.uber.org/fx/fxtest"

	"github.com/rahacloud/joghd/internal/alerter"
	"github.com/rahacloud/joghd/internal/checker"
	"github.com/rahacloud/joghd/internal/config"
	"github.com/rahacloud/joghd/internal/domain"
	"github.com/rahacloud/joghd/internal/metrics"
	"github.com/rahacloud/joghd/internal/scheduler"
)

// stubChecker reports a fixed outcome for every target.
type stubChecker struct {
	success bool
	checks  atomic.Int64
}

func (s *stubChecker) Check(_ context.Context, target domain.Target) domain.CheckResult {
	s.checks.Add(1)

	result := domain.CheckResult{Target: target, Success: s.success, Attempts: 1, Timestamp: time.Now()}
	if s.success {
		result.ActualStatus = target.ExpectedStatus
	} else {
		result.ActualStatus = 500
		result.Error = errors.New("status mismatch")
	}

	return result
}

func (s *stubChecker) CheckAll(ctx context.Context, targets []domain.Target) []domain.CheckResult {
	results := make([]domain.CheckResult, len(targets))
	for i, t := range targets {
		results[i] = s.Check(ctx, t)
	}

	return results
}

// stubAlerter records the alerts it was asked to send.
type stubAlerter struct {
	mu       sync.Mutex
	sent     []domain.Alert
	err      error
	deadline bool
}

func (s *stubAlerter) Send(ctx context.Context, alert domain.Alert) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, s.deadline = ctx.Deadline()
	s.sent = append(s.sent, alert)

	return s.err
}

func (s *stubAlerter) Name() string { return "stub" }

func (s *stubAlerter) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return len(s.sent)
}

// stubShutdowner records the shutdown request issued by oneshot mode.
type stubShutdowner struct {
	called atomic.Bool
}

func (s *stubShutdowner) Shutdown(...fx.ShutdownOption) error {
	s.called.Store(true)

	return nil
}

func testTargets() []domain.Target {
	return []domain.Target{{
		Name:           "api",
		URL:            "https://api.example.com/health",
		ExpectedStatus: 200,
		Method:         "GET",
		Interval:       20 * time.Millisecond,
		Timeout:        time.Second,
	}}
}

func TestRunOneshotExitCodes(t *testing.T) {
	tests := []struct {
		name         string
		success      bool
		wantExitCode int
		wantAlerts   int
	}{
		{name: "all healthy", success: true, wantExitCode: 0, wantAlerts: 0},
		{name: "a failure", success: false, wantExitCode: 1, wantAlerts: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chk := &stubChecker{success: tc.success}
			alt := &stubAlerter{}

			got := runOneshot(t.Context(), chk, alt, testTargets())

			if got != tc.wantExitCode {
				t.Errorf("exit code = %d, want %d", got, tc.wantExitCode)
			}
			if alt.count() != tc.wantAlerts {
				t.Errorf("sent %d alerts, want %d", alt.count(), tc.wantAlerts)
			}
		})
	}
}

// A failing alerter must not change the exit code: the health check result is
// what the caller acts on.
func TestRunOneshotIgnoresAlerterFailure(t *testing.T) {
	chk := &stubChecker{success: false}
	alt := &stubAlerter{err: errors.New("telegram is down")}

	if got := runOneshot(t.Context(), chk, alt, testTargets()); got != 1 {
		t.Errorf("exit code = %d, want 1", got)
	}
}

func TestRunOneshotBoundsAlertSends(t *testing.T) {
	chk := &stubChecker{success: false}
	alt := &stubAlerter{}

	runOneshot(t.Context(), chk, alt, testTargets())

	if !alt.deadline {
		t.Error("alert was sent with a context that has no deadline")
	}
}

func TestProvideAlerter(t *testing.T) {
	tests := []struct {
		name      string
		alerters  map[string]config.AlerterConfig
		wantErr   bool
		wantNames []string
	}{
		{
			name:     "no alerters configured",
			alerters: map[string]config.AlerterConfig{},
		},
		{
			name: "disabled alerters are skipped",
			alerters: map[string]config.AlerterConfig{
				"raha": {Type: config.AlerterTypeTelegram, Enabled: false},
			},
		},
		{
			name: "telegram and mattermost are both built",
			alerters: map[string]config.AlerterConfig{
				"raha": {Type: config.AlerterTypeTelegram, Enabled: true, BotToken: "t", ChatID: "-100", Timeout: time.Second},
				"team": {Type: config.AlerterTypeMattermost, Enabled: true, WebhookURL: "https://mm.example.com/hooks/x", Timeout: time.Second},
			},
			wantNames: []string{"telegram:raha", "mattermost:team"},
		},
		{
			name: "unsupported type is rejected",
			alerters: map[string]config.AlerterConfig{
				"raha": {Type: "carrier-pigeon", Enabled: true},
			},
			wantErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := provideAlerter(tc.alerters, metrics.Nop{})

			if tc.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}

				return
			}
			if err != nil {
				t.Fatalf("provideAlerter: %v", err)
			}

			name := got.Name()
			for _, want := range tc.wantNames {
				if !strings.Contains(name, want) {
					t.Errorf("composite name = %q, want it to include %q", name, want)
				}
			}
		})
	}
}

func TestProvideLoggerLevels(t *testing.T) {
	tests := []struct {
		level      string
		wantDebug  bool
		wantErrors bool
	}{
		{level: "debug", wantDebug: true, wantErrors: true},
		{level: "info", wantDebug: false, wantErrors: true},
		{level: "error", wantDebug: false, wantErrors: true},
	}

	for _, tc := range tests {
		t.Run(tc.level, func(t *testing.T) {
			logger := provideLogger(config.AppConfig{LogLevel: tc.level})

			if got := logger.Enabled(t.Context(), slog.LevelDebug); got != tc.wantDebug {
				t.Errorf("debug enabled = %v, want %v", got, tc.wantDebug)
			}
			if got := logger.Enabled(t.Context(), slog.LevelError); got != tc.wantErrors {
				t.Errorf("error enabled = %v, want %v", got, tc.wantErrors)
			}
		})
	}
}

func TestRegisterRunnerRejectsUnknownMode(t *testing.T) {
	err := registerRunner(fxtest.NewLifecycle(t), &stubShutdowner{},
		config.AppConfig{Mode: "sometimes"}, &stubChecker{}, &stubAlerter{}, testTargets(), nil)

	if err == nil {
		t.Fatal("expected an error for an unknown mode, got nil")
	}
}

func TestRegisterRunnerOneshotShutsDown(t *testing.T) {
	lc := fxtest.NewLifecycle(t)
	shutdowner := &stubShutdowner{}
	chk := &stubChecker{success: true}

	if err := registerRunner(lc, shutdowner, config.AppConfig{Mode: config.ModeOneshot},
		chk, &stubAlerter{}, testTargets(), nil); err != nil {
		t.Fatalf("registerRunner: %v", err)
	}

	lc.RequireStart()
	defer lc.RequireStop()

	waitFor(t, func() bool { return shutdowner.called.Load() }, "oneshot did not request shutdown")
}

func TestRegisterRunnerContinuousRunsAndStops(t *testing.T) {
	lc := fxtest.NewLifecycle(t)
	chk := &stubChecker{success: true}
	alt := &stubAlerter{}
	sched := scheduler.New(chk, alt, metrics.Nop{}, testTargets(), config.AppConfig{})

	if err := registerRunner(lc, &stubShutdowner{}, config.AppConfig{Mode: config.ModeContinuous},
		chk, alt, testTargets(), sched); err != nil {
		t.Fatalf("registerRunner: %v", err)
	}

	lc.RequireStart()
	waitFor(t, func() bool { return chk.checks.Load() >= 2 }, "the scheduler did not keep checking")

	// Stopping must cancel the scheduler's context rather than leaving it
	// running in the background.
	lc.RequireStop()

	settled := chk.checks.Load()
	time.Sleep(100 * time.Millisecond)

	if after := chk.checks.Load(); after > settled+1 {
		t.Errorf("checks went from %d to %d after stop; the loop was not cancelled", settled, after)
	}
}

func TestProvideHTTPClientClosesOnStop(t *testing.T) {
	lc := fxtest.NewLifecycle(t)

	client := provideHTTPClient(lc, config.HTTPConfig{Timeout: time.Second, UserAgent: "Joghd/test"})
	if client == nil {
		t.Fatal("provideHTTPClient returned nil")
	}

	lc.RequireStart()
	// RequireStop fails the test if the OnStop hook returns an error.
	lc.RequireStop()
}

func TestProvideChecker(t *testing.T) {
	c := provideChecker(&stubHTTPClient{}, config.DefaultRetry(), config.AppConfig{Concurrency: 2})
	if c == nil {
		t.Fatal("provideChecker returned nil")
	}

	results := c.CheckAll(t.Context(), testTargets())
	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
}

// stubHTTPClient satisfies checker.HTTPClient without touching the network.
type stubHTTPClient struct{}

func (stubHTTPClient) Execute(_ context.Context, _ checker.Request) (checker.Response, error) {
	return checker.Response{StatusCode: 200, Latency: time.Millisecond}, nil
}

var _ checker.HTTPClient = stubHTTPClient{}
var _ alerter.Alerter = (*stubAlerter)(nil)
var _ checker.Checker = (*stubChecker)(nil)

func waitFor(t *testing.T, cond func() bool, msg string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}

		time.Sleep(5 * time.Millisecond)
	}

	t.Fatal(msg)
}
