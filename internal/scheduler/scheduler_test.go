package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/rahacloud/joghd/internal/config"
	"github.com/rahacloud/joghd/internal/domain"
)

// stubChecker reports a fixed outcome and counts the checks it performed.
type stubChecker struct {
	mu      sync.Mutex
	success bool
	checks  int
	at      []time.Time
}

func (s *stubChecker) Check(_ context.Context, target domain.Target) domain.CheckResult {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.checks++
	s.at = append(s.at, time.Now())

	result := domain.CheckResult{Target: target, Success: s.success, Timestamp: time.Now(), Attempts: 1}
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

func (s *stubChecker) setSuccess(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.success = v
}

func (s *stubChecker) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.checks
}

// gaps returns the interval between consecutive checks.
func (s *stubChecker) gaps() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()

	gaps := make([]time.Duration, 0, max(len(s.at)-1, 0))
	for i := 1; i < len(s.at); i++ {
		gaps = append(gaps, s.at[i].Sub(s.at[i-1]))
	}

	return gaps
}

// recordingAlerter captures the alert types it was asked to send.
type recordingAlerter struct {
	mu       sync.Mutex
	sent     []domain.AlertType
	err      error
	deadline bool // records whether the send context carried a deadline
}

func (r *recordingAlerter) Send(ctx context.Context, alert domain.Alert) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, hasDeadline := ctx.Deadline()
	r.deadline = hasDeadline
	r.sent = append(r.sent, alert.Type)

	return r.err
}

func (r *recordingAlerter) Name() string { return "recording" }

func (r *recordingAlerter) types() []domain.AlertType {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]domain.AlertType(nil), r.sent...)
}

func testTarget() domain.Target {
	return domain.Target{
		Name:           "api",
		URL:            "https://api.example.com/health",
		ExpectedStatus: 200,
		Interval:       10 * time.Millisecond,
		Timeout:        time.Second,
	}
}

func newScheduler(chk *stubChecker, alt *recordingAlerter, reminderMultiplier int) *Scheduler {
	return New(chk, alt, []domain.Target{testTarget()}, config.AppConfig{ReminderMultiplier: reminderMultiplier})
}

func TestCheckAndAlertTransitions(t *testing.T) {
	tests := []struct {
		name      string
		previous  domain.HealthStatus
		success   bool
		wantAlert []domain.AlertType
	}{
		{
			name:      "first check healthy sends nothing",
			previous:  domain.StatusUnknown,
			success:   true,
			wantAlert: nil,
		},
		{
			name:      "first check unhealthy alerts",
			previous:  domain.StatusUnknown,
			success:   false,
			wantAlert: []domain.AlertType{domain.AlertTypeFailure},
		},
		{
			name:      "healthy to unhealthy alerts",
			previous:  domain.StatusHealthy,
			success:   false,
			wantAlert: []domain.AlertType{domain.AlertTypeFailure},
		},
		{
			name:      "unhealthy to healthy recovers",
			previous:  domain.StatusUnhealthy,
			success:   true,
			wantAlert: []domain.AlertType{domain.AlertTypeRecovery},
		},
		{
			name:      "still healthy stays quiet",
			previous:  domain.StatusHealthy,
			success:   true,
			wantAlert: nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chk := &stubChecker{success: tc.success}
			alt := &recordingAlerter{}
			s := newScheduler(chk, alt, 0)

			// lastAlertTime is left at the zero value so no reminder is due.
			state := targetState{status: tc.previous}
			s.checkAndAlert(t.Context(), testTarget(), &state)

			got := alt.types()
			if len(got) != len(tc.wantAlert) {
				t.Fatalf("sent %v, want %v", got, tc.wantAlert)
			}
			for i := range got {
				if got[i] != tc.wantAlert[i] {
					t.Errorf("alert[%d] = %v, want %v", i, got[i], tc.wantAlert[i])
				}
			}

			wantStatus := domain.StatusUnhealthy
			if tc.success {
				wantStatus = domain.StatusHealthy
			}
			if state.status != wantStatus {
				t.Errorf("state.status = %v, want %v", state.status, wantStatus)
			}
		})
	}
}

func TestCheckAndAlertRemindersRespectMultiplier(t *testing.T) {
	target := testTarget()

	tests := []struct {
		name               string
		reminderMultiplier int
		sinceLastAlert     time.Duration
		wantReminder       bool
	}{
		{name: "disabled", reminderMultiplier: 0, sinceLastAlert: time.Hour, wantReminder: false},
		{name: "not due yet", reminderMultiplier: 6, sinceLastAlert: 0, wantReminder: false},
		{name: "due", reminderMultiplier: 2, sinceLastAlert: 10 * target.Interval, wantReminder: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			chk := &stubChecker{success: false}
			alt := &recordingAlerter{}
			s := newScheduler(chk, alt, tc.reminderMultiplier)

			state := targetState{
				status:        domain.StatusUnhealthy,
				lastAlertTime: time.Now().Add(-tc.sinceLastAlert),
			}
			s.checkAndAlert(t.Context(), target, &state)

			got := alt.types()
			if tc.wantReminder {
				if len(got) != 1 || got[0] != domain.AlertTypeReminder {
					t.Errorf("sent %v, want a single reminder", got)
				}
			} else if len(got) != 0 {
				t.Errorf("sent %v, want nothing", got)
			}
		})
	}
}

// Recovery must reset the reminder clock, otherwise the next failure is
// immediately treated as an overdue reminder.
func TestRecoveryResetsReminderClock(t *testing.T) {
	chk := &stubChecker{success: true}
	alt := &recordingAlerter{}
	s := newScheduler(chk, alt, 1)

	state := targetState{status: domain.StatusUnhealthy, lastAlertTime: time.Now().Add(-time.Hour)}
	s.checkAndAlert(t.Context(), testTarget(), &state)

	if !state.lastAlertTime.IsZero() {
		t.Errorf("lastAlertTime = %v, want the zero value after recovery", state.lastAlertTime)
	}
}

// A slow alerter must not be able to block health checks forever.
func TestAlertsAreSentWithADeadline(t *testing.T) {
	chk := &stubChecker{success: false}
	alt := &recordingAlerter{}
	s := newScheduler(chk, alt, 0)

	state := targetState{status: domain.StatusHealthy}
	s.checkAndAlert(t.Context(), testTarget(), &state)

	if !alt.deadline {
		t.Error("alert was sent with a context that has no deadline")
	}
}

func TestSendSurvivesAlerterErrors(t *testing.T) {
	chk := &stubChecker{success: false}
	alt := &recordingAlerter{err: errors.New("telegram is down")}
	s := newScheduler(chk, alt, 0)

	state := targetState{status: domain.StatusHealthy}
	s.checkAndAlert(t.Context(), testTarget(), &state)

	if state.status != domain.StatusUnhealthy {
		t.Errorf("state.status = %v, want unhealthy even when alerting failed", state.status)
	}
}

// Two targets sharing a URL must keep independent state.
func TestTargetsSharingAURLDoNotInterfere(t *testing.T) {
	chk := &stubChecker{success: false}
	alt := &recordingAlerter{}

	first := testTarget()
	first.Name = "api-200"
	second := testTarget()
	second.Name = "api-204"
	second.ExpectedStatus = 204

	s := New(chk, alt, []domain.Target{first, second}, config.AppConfig{})

	stateA := targetState{status: domain.StatusUnknown}
	stateB := targetState{status: domain.StatusUnknown}

	s.checkAndAlert(t.Context(), first, &stateA)
	chk.setSuccess(true)
	s.checkAndAlert(t.Context(), second, &stateB)

	if stateA.status != domain.StatusUnhealthy {
		t.Errorf("first target status = %v, want unhealthy", stateA.status)
	}
	if stateB.status != domain.StatusHealthy {
		t.Errorf("second target status = %v, want healthy", stateB.status)
	}

	// Only the first target's failure should have alerted; the second was
	// healthy on its first check.
	if got := alt.types(); len(got) != 1 || got[0] != domain.AlertTypeFailure {
		t.Errorf("sent %v, want a single failure alert", got)
	}
}

func TestStartStopsOnContextCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		chk := &stubChecker{success: true}
		alt := &recordingAlerter{}
		s := newScheduler(chk, alt, 0)

		ctx, cancel := context.WithCancel(t.Context())

		done := make(chan error, 1)
		go func() { done <- s.Start(ctx) }()

		// Six intervals of fake time, so the loop must have ticked repeatedly
		// even at the upper end of the jitter range.
		synctest.Sleep(6 * testTarget().Interval)
		cancel()

		// A Start that ignores cancellation deadlocks the bubble, which fails
		// the test without a timeout having to be guessed at.
		if err := <-done; err != nil {
			t.Errorf("Start returned %v, want nil", err)
		}

		if chk.count() < 2 {
			t.Errorf("performed %d checks, expected the loop to fire repeatedly", chk.count())
		}
	})
}

// Jitter keeps targets that share an interval from probing in lockstep, but it
// must stay inside the operator's configured interval budget. A fake clock is
// what makes the bound assertable at all.
func TestTickIntervalIsJittered(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		chk := &stubChecker{success: true}
		s := newScheduler(chk, &recordingAlerter{}, 0)

		ctx, cancel := context.WithCancel(t.Context())

		done := make(chan error, 1)
		go func() { done <- s.Start(ctx) }()

		interval := testTarget().Interval
		synctest.Sleep(50 * interval)
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Start returned %v, want nil", err)
		}

		gaps := chk.gaps()
		if len(gaps) < 10 {
			t.Fatalf("recorded %d gaps, want at least 10", len(gaps))
		}

		upper := interval + time.Duration(float64(interval)*tickJitterFraction)

		jittered := false
		for i, gap := range gaps {
			if gap < interval || gap > upper {
				t.Errorf("gap[%d] = %s, want within [%s, %s]", i, gap, interval, upper)
			}
			if gap != interval {
				jittered = true
			}
		}

		if !jittered {
			t.Error("every gap was exactly the interval; the jitter is not being applied")
		}
	})
}
