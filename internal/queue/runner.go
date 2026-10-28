// Package queue runs one worker goroutine per key (a session), so a slow or
// failing session never delays the others. The work itself is supplied by the
// caller; messages are persisted in the database, which is the real queue.
package queue

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Next tells the worker what to do after a unit of work.
type Next struct {
	// Delay is how long to sleep before working again. A Wake interrupts it.
	// Zero means more work is ready right now.
	Delay time.Duration
	// Stop ends the worker for good (e.g. its session no longer exists).
	Stop bool
}

// WorkFunc performs one unit of work for a key.
type WorkFunc func(ctx context.Context, key uuid.UUID) Next

// Runner owns the workers.
type Runner struct {
	work   WorkFunc
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu      sync.Mutex
	workers map[uuid.UUID]chan struct{}
}

// NewRunner creates a Runner. Workers start lazily on the first Wake.
func NewRunner(work WorkFunc) *Runner {
	ctx, cancel := context.WithCancel(context.Background())
	return &Runner{work: work, ctx: ctx, cancel: cancel, workers: make(map[uuid.UUID]chan struct{})}
}

// Wake makes the worker of a key run now, starting it if needed.
func (r *Runner) Wake(key uuid.UUID) {
	if r.ctx.Err() != nil {
		return
	}

	r.mu.Lock()
	wake, exists := r.workers[key]
	if !exists {
		// A new worker works immediately, so it needs no extra signal.
		wake = make(chan struct{}, 1)
		r.workers[key] = wake
		r.wg.Add(1)
		go r.run(key, wake)
		r.mu.Unlock()
		return
	}
	r.mu.Unlock()

	select {
	case wake <- struct{}{}:
	default: // a wake-up is already pending
	}
}

// Stop cancels every worker and waits for the work in progress to return.
func (r *Runner) Stop() {
	r.cancel()
	r.wg.Wait()
}

func (r *Runner) run(key uuid.UUID, wake chan struct{}) {
	defer r.wg.Done()
	defer func() {
		r.mu.Lock()
		if r.workers[key] == wake {
			delete(r.workers, key)
		}
		r.mu.Unlock()
	}()

	for {
		if r.ctx.Err() != nil {
			return
		}
		next := r.work(r.ctx, key)
		if next.Stop {
			return
		}
		if next.Delay <= 0 {
			continue
		}

		timer := time.NewTimer(next.Delay)
		select {
		case <-r.ctx.Done():
			timer.Stop()
			return
		case <-wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}
