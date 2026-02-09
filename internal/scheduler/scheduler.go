package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/raha-io/joghd/internal/alerter"
	"github.com/raha-io/joghd/internal/checker"
	"github.com/raha-io/joghd/internal/domain"
)

// Scheduler manages periodic health checks for multiple targets.
type Scheduler struct {
	checker checker.Checker
	alerter alerter.Alerter
	targets []domain.Target

	mu     sync.RWMutex
	states map[string]domain.HealthStatus // target URL -> health status
}

// New creates a new scheduler.
func New(chk checker.Checker, alt alerter.Alerter, targets []domain.Target) *Scheduler {
	states := make(map[string]domain.HealthStatus)
	for _, t := range targets {
		states[t.URL] = domain.StatusUnknown
	}

	return &Scheduler{
		checker: chk,
		alerter: alt,
		targets: targets,
		states:  states,
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
	previousStatus := s.states[target.URL]
	var currentStatus domain.HealthStatus
	if result.Success {
		currentStatus = domain.StatusHealthy
	} else {
		currentStatus = domain.StatusUnhealthy
	}
	s.states[target.URL] = currentStatus
	s.mu.Unlock()

	// Detect state transitions
	statusChanged := previousStatus != currentStatus && previousStatus != domain.StatusUnknown

	if !result.Success {
		// Send failure alert on first failure or if still failing
		if previousStatus != domain.StatusUnhealthy {
			alert := domain.NewFailureAlert(result)
			if err := s.alerter.Send(ctx, alert); err != nil {
				slog.Error("Failed to send failure alert", "target", target.Name, "error", err)
			} else {
				slog.Warn("Sent failure alert", "target", target.Name)
			}
		} else {
			slog.Warn("Target still unhealthy",
				"target", target.Name,
				"status", result.ActualStatus,
				"expected", target.ExpectedStatus,
			)
		}
	} else if statusChanged && currentStatus == domain.StatusHealthy {
		// Send recovery alert
		alert := domain.NewRecoveryAlert(result)
		if err := s.alerter.Send(ctx, alert); err != nil {
			slog.Error("Failed to send recovery alert", "target", target.Name, "error", err)
		} else {
			slog.Info("Sent recovery alert", "target", target.Name)
		}
	} else if result.Success {
		slog.Debug("Target healthy",
			"target", target.Name,
			"status", result.ActualStatus,
			"latency", result.Latency.Round(time.Millisecond),
		)
	}
}

// GetStatus returns the current health status of a target.
func (s *Scheduler) GetStatus(targetURL string) domain.HealthStatus {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.states[targetURL]
}
