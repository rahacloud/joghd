// Package scheduler runs health checks on a repeating schedule and decides
// which state changes are worth alerting about.
//
// Each target gets its own goroutine and its own state, so targets never
// interfere with one another. Alerts fire on the transition into failure, on
// recovery, and on a reminder cadence derived from the target's interval;
// a steady-state failure is logged rather than re-alerted. Sleeps between
// checks carry a small random jitter so targets sharing an interval do not
// probe in lockstep.
package scheduler

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/rahacloud/joghd/internal/alerter"
	"github.com/rahacloud/joghd/internal/checker"
	"github.com/rahacloud/joghd/internal/config"
	"github.com/rahacloud/joghd/internal/domain"
	"github.com/rahacloud/joghd/internal/metrics"
)

// tickJitterFraction is the largest share of a target's interval that may be
// added to any one sleep between checks. Without it every target configured
// with the same interval fires in lockstep for the life of the process, so a
// group of endpoints behind one gateway all get probed on the same instant.
const tickJitterFraction = 0.1

// targetState tracks one target between checks. Each instance belongs to the
// single goroutine running that target's loop, so it needs no locking and two
// targets sharing a URL cannot interfere with each other.
type targetState struct {
	status        domain.HealthStatus
	lastAlertTime time.Time

	// rand is owned by this target's goroutine and created on first use.
	// math/rand/v2's *Rand is not safe for concurrent use, so each loop gets
	// its own rather than contending on the global source.
	rand *rand.Rand
}

// Scheduler manages periodic health checks for multiple targets.
type Scheduler struct {
	checker            checker.Checker
	alerter            alerter.Alerter
	recorder           metrics.Recorder
	targets            []domain.Target
	reminderMultiplier int
}

// New creates a new scheduler.
func New(chk checker.Checker, alt alerter.Alerter, rec metrics.Recorder, targets []domain.Target, appCfg config.AppConfig) *Scheduler {
	for _, t := range targets {
		rec.RegisterTarget(t)
	}

	return &Scheduler{
		checker:            chk,
		alerter:            alt,
		recorder:           rec,
		targets:            targets,
		reminderMultiplier: appCfg.ReminderMultiplier,
	}
}

// Start begins the scheduling loop. Blocks until context is cancelled.
func (s *Scheduler) Start(ctx context.Context) error {
	var wg sync.WaitGroup

	for _, target := range s.targets {
		wg.Go(func() {
			s.runTargetLoop(ctx, target)
		})
	}

	slog.Info("Scheduler started", "targets", len(s.targets))

	// Wait for all goroutines to finish
	wg.Wait()

	slog.Info("Scheduler stopped")

	return nil
}

func (s *Scheduler) runTargetLoop(ctx context.Context, target domain.Target) {
	state := targetState{status: domain.StatusUnknown}

	// Run initial check immediately
	s.checkAndAlert(ctx, target, &state)

	// A timer rather than a ticker: the delay is recomputed every round so
	// the jitter is re-drawn instead of being a fixed per-target offset.
	timer := time.NewTimer(state.nextDelay(target.Interval))
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			s.checkAndAlert(ctx, target, &state)
			timer.Reset(state.nextDelay(target.Interval))
		}
	}
}

// nextDelay returns interval plus up to tickJitterFraction of it.
//
// (*Rand).N is new in Go 1.27; being generic it draws the offset directly as a
// time.Duration, so the jitter cannot be computed in the wrong unit the way an
// int64 round trip through Int64N invites.
func (t *targetState) nextDelay(interval time.Duration) time.Duration {
	spread := time.Duration(float64(interval) * tickJitterFraction)
	if spread <= 0 {
		return interval
	}

	if t.rand == nil {
		t.rand = rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	}

	return interval + t.rand.N(spread)
}

func (s *Scheduler) checkAndAlert(ctx context.Context, target domain.Target, state *targetState) {
	result := s.checker.Check(ctx, target)
	s.recorder.ObserveCheck(result)

	currentStatus := domain.StatusUnhealthy
	if result.Success {
		currentStatus = domain.StatusHealthy
	}

	previousStatus := state.status
	state.status = currentStatus

	switch {
	case !result.Success && previousStatus != domain.StatusUnhealthy:
		// First failure, or first check of an already-broken target.
		state.lastAlertTime = time.Now()
		s.send(ctx, target, domain.NewFailureAlert(result), "failure", slog.LevelWarn)

	case !result.Success && s.reminderDue(state, target):
		state.lastAlertTime = time.Now()
		s.send(ctx, target, domain.NewReminderAlert(result), "reminder", slog.LevelWarn)

	case !result.Success:
		slog.Warn("Target still unhealthy",
			"target", target.Name,
			"status", result.ActualStatus,
			"expected", target.ExpectedStatus,
		)

	case previousStatus == domain.StatusUnhealthy:
		// Recovered. A target that starts healthy has no recovery to report.
		state.lastAlertTime = time.Time{}
		s.send(ctx, target, domain.NewRecoveryAlert(result), "recovery", slog.LevelInfo)

	default:
		slog.Debug("Target healthy",
			"target", target.Name,
			"status", result.ActualStatus,
			"latency", result.Latency.Round(time.Millisecond),
		)
	}
}

// reminderDue reports whether a still-unhealthy target is due another alert.
func (s *Scheduler) reminderDue(state *targetState, target domain.Target) bool {
	if s.reminderMultiplier <= 0 {
		return false
	}

	return time.Since(state.lastAlertTime) >= target.Interval*time.Duration(s.reminderMultiplier)
}

// send fans an alert out to the configured alerters under a bounded context so
// that a slow alerter cannot stop this target from being checked again.
func (s *Scheduler) send(ctx context.Context, target domain.Target, alert domain.Alert, kind string, level slog.Level) {
	ctx, cancel := context.WithTimeout(ctx, config.AlertFanoutTimeout)
	defer cancel()

	if err := s.alerter.Send(ctx, alert); err != nil {
		slog.Error("Failed to send alert", "kind", kind, "target", target.Name, "alert_id", alert.ID, "error", err)

		return
	}

	slog.Log(ctx, level, "Sent alert", "kind", kind, "target", target.Name, "alert_id", alert.ID)
}
