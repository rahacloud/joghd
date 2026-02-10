package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/raha-io/joghd/internal/alerter"
	"github.com/raha-io/joghd/internal/checker"
	"github.com/raha-io/joghd/internal/config"
	"github.com/raha-io/joghd/internal/domain"
)

type targetState struct {
	status        domain.HealthStatus
	lastAlertTime time.Time
}

// Scheduler manages periodic health checks for multiple targets.
type Scheduler struct {
	checker            checker.Checker
	alerter            alerter.Alerter
	targets            []domain.Target
	reminderMultiplier int

	mu     sync.RWMutex
	states map[string]*targetState // target URL -> state
}

// New creates a new scheduler.
func New(chk checker.Checker, alt alerter.Alerter, targets []domain.Target, appCfg config.AppConfig) *Scheduler {
	states := make(map[string]*targetState)
	for _, t := range targets {
		states[t.URL] = &targetState{status: domain.StatusUnknown}
	}

	return &Scheduler{
		checker:            chk,
		alerter:            alt,
		targets:            targets,
		reminderMultiplier: appCfg.ReminderMultiplier,
		states:             states,
	}
}

// Start begins the scheduling loop. Blocks until context is cancelled.
func (s *Scheduler) Start(ctx context.Context) error {
	var wg sync.WaitGroup

	for _, target := range s.targets {
		wg.Add(1)
		go func(t domain.Target) {
			defer wg.Done()
			s.runTargetLoop(ctx, t)
		}(target)
	}

	slog.Info("Scheduler started", "targets", len(s.targets))

	// Wait for all goroutines to finish
	wg.Wait()

	slog.Info("Scheduler stopped")
	return nil
}

func (s *Scheduler) runTargetLoop(ctx context.Context, target domain.Target) {
	ticker := time.NewTicker(target.Interval)
	defer ticker.Stop()

	// Run initial check immediately
	s.checkAndAlert(ctx, target)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.checkAndAlert(ctx, target)
		}
	}
}

func (s *Scheduler) checkAndAlert(ctx context.Context, target domain.Target) {
	result := s.checker.Check(ctx, target)

	s.mu.Lock()
	state := s.states[target.URL]
	previousStatus := state.status
	var currentStatus domain.HealthStatus
	if result.Success {
		currentStatus = domain.StatusHealthy
	} else {
		currentStatus = domain.StatusUnhealthy
	}
	state.status = currentStatus

	// Detect state transitions
	statusChanged := previousStatus != currentStatus && previousStatus != domain.StatusUnknown

	if !result.Success {
		if previousStatus != domain.StatusUnhealthy {
			// First failure: send failure alert
			state.lastAlertTime = time.Now()
			s.mu.Unlock()

			alert := domain.NewFailureAlert(result)
			if err := s.alerter.Send(ctx, alert); err != nil {
				slog.Error("Failed to send failure alert", "target", target.Name, "error", err)
			} else {
				slog.Warn("Sent failure alert", "target", target.Name)
			}
		} else if s.reminderMultiplier > 0 && time.Since(state.lastAlertTime) >= target.Interval*time.Duration(s.reminderMultiplier) {
			// Still unhealthy and reminder interval elapsed
			state.lastAlertTime = time.Now()
			s.mu.Unlock()

			alert := domain.NewReminderAlert(result)
			if err := s.alerter.Send(ctx, alert); err != nil {
				slog.Error("Failed to send reminder alert", "target", target.Name, "error", err)
			} else {
				slog.Warn("Sent reminder alert", "target", target.Name)
			}
		} else {
			s.mu.Unlock()

			slog.Warn("Target still unhealthy",
				"target", target.Name,
				"status", result.ActualStatus,
				"expected", target.ExpectedStatus,
			)
		}
	} else if statusChanged && currentStatus == domain.StatusHealthy {
		state.lastAlertTime = time.Time{}
		s.mu.Unlock()

		alert := domain.NewRecoveryAlert(result)
		if err := s.alerter.Send(ctx, alert); err != nil {
			slog.Error("Failed to send recovery alert", "target", target.Name, "error", err)
		} else {
			slog.Info("Sent recovery alert", "target", target.Name)
		}
	} else {
		s.mu.Unlock()

		if result.Success {
			slog.Debug("Target healthy",
				"target", target.Name,
				"status", result.ActualStatus,
				"latency", result.Latency.Round(time.Millisecond),
			)
		}
	}
}

// GetStatus returns the current health status of a target.
func (s *Scheduler) GetStatus(targetURL string) domain.HealthStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.states[targetURL].status
}
