package email

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

const (
	pollInterval = 5 * time.Second
	// sendLease is how long a claimed email is reserved before another claim may
	// retake it (covers a crash mid-send).
	sendLease     = 2 * time.Minute
	settleTimeout = 10 * time.Second
	maxBackoff    = 6 * time.Hour
)

// WorkerConfig tunes delivery.
type WorkerConfig struct {
	// MaxAttempts is how many sends are tried before an email fails for good.
	MaxAttempts int
	// RetryBase is the wait after the first failure; it doubles on each retry.
	RetryBase time.Duration
}

// Worker claims due emails and sends them, retrying with exponential backoff.
type Worker struct {
	repo   Repository
	sender Sender
	cfg    WorkerConfig
	log    *slog.Logger
	now    func() time.Time

	wake   chan struct{}
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewWorker creates a Worker. Call Start to run it and Stop to shut it down.
func NewWorker(repo Repository, sender Sender, cfg WorkerConfig, log *slog.Logger) *Worker {
	return &Worker{repo: repo, sender: sender, cfg: cfg, log: log, now: time.Now, wake: make(chan struct{}, 1)}
}

// Wake makes the worker look for due emails now.
func (w *Worker) Wake() {
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Start runs the worker in the background until ctx ends or Stop is called.
func (w *Worker) Start(ctx context.Context) {
	ctx, w.cancel = context.WithCancel(ctx)
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.run(ctx)
	}()
	w.log.Info("email worker started")
}

// Stop ends the worker and waits for the send in progress.
func (w *Worker) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
	w.wg.Wait()
}

func (w *Worker) run(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-w.wake:
		case <-timer.C:
		}
		for ctx.Err() == nil && w.ProcessOne(ctx) {
		}
		timer.Reset(pollInterval)
	}
}

// ProcessOne claims and handles one due email. It reports whether it found one.
func (w *Worker) ProcessOne(ctx context.Context) bool {
	e, found, err := w.repo.Claim(ctx, sendLease)
	if err != nil {
		if ctx.Err() == nil {
			w.log.Error("claim email", "error", err)
		}
		return false
	}
	if !found {
		return false
	}

	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
	defer cancel()

	if e.Attempts > w.cfg.MaxAttempts {
		// Claimed again after crashes during the last attempts: give up.
		w.fail(settleCtx, e, "abandoned after too many attempts")
		return true
	}
	rendered, err := Render(e.Template, e.Data)
	if err != nil {
		w.fail(settleCtx, e, "render failed")
		return true
	}

	err = w.sender.Send(ctx, Message{To: e.To, Template: e.Template, Subject: rendered.Subject, HTML: rendered.HTML, Text: rendered.Text})
	switch {
	case err == nil:
		if err := w.repo.MarkSent(settleCtx, e.ID); err != nil {
			w.log.Error("mark email sent", "email_id", e.ID, "error", err)
		}
	case ctx.Err() != nil:
		// Shutting down mid-send: the lease expires and the email is retried.
	case errors.Is(err, ErrPermanent):
		w.fail(settleCtx, e, err.Error())
	case e.Attempts >= w.cfg.MaxAttempts:
		w.fail(settleCtx, e, err.Error())
	default:
		next := w.now().Add(backoff(w.cfg.RetryBase, e.Attempts))
		w.log.Warn("email send failed, will retry", "email_id", e.ID, "template", e.Template, "attempt", e.Attempts, "error", err)
		if err := w.repo.Reschedule(settleCtx, e.ID, next, err.Error()); err != nil {
			w.log.Error("reschedule email", "email_id", e.ID, "error", err)
		}
	}
	return true
}

func (w *Worker) fail(ctx context.Context, e Email, reason string) {
	w.log.Error("email failed", "email_id", e.ID, "template", e.Template, "attempts", e.Attempts, "reason", reason)
	if err := w.repo.MarkFailed(ctx, e.ID, reason); err != nil {
		w.log.Error("mark email failed", "email_id", e.ID, "error", err)
	}
}

// backoff is base * 2^(attempt-1), capped.
func backoff(base time.Duration, attempt int) time.Duration {
	d := base
	for i := 1; i < attempt && d < maxBackoff; i++ {
		d *= 2
	}
	return min(d, maxBackoff)
}
