package queue

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

func waitFor(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func TestWakeRunsTheWorkerAndSleepsUntilWokenAgain(t *testing.T) {
	var runs atomic.Int32
	runner := NewRunner(func(context.Context, uuid.UUID) Next {
		runs.Add(1)
		return Next{Delay: time.Hour}
	})
	defer runner.Stop()

	key := uuid.New()
	runner.Wake(key)
	waitFor(t, func() bool { return runs.Load() == 1 })

	time.Sleep(50 * time.Millisecond)
	if runs.Load() != 1 {
		t.Fatal("the worker must sleep for its delay")
	}

	runner.Wake(key)
	waitFor(t, func() bool { return runs.Load() == 2 })
}

func TestZeroDelayKeepsWorkingUntilIdle(t *testing.T) {
	var remaining atomic.Int32
	remaining.Store(5)
	var runs atomic.Int32
	runner := NewRunner(func(context.Context, uuid.UUID) Next {
		runs.Add(1)
		if remaining.Add(-1) > 0 {
			return Next{}
		}
		return Next{Delay: time.Hour}
	})
	defer runner.Stop()

	runner.Wake(uuid.New())
	waitFor(t, func() bool { return runs.Load() == 5 })
}

func TestWorkersAreIndependent(t *testing.T) {
	slowStarted := make(chan struct{})
	release := make(chan struct{})
	slow, fast := uuid.New(), uuid.New()
	var fastRuns atomic.Int32

	runner := NewRunner(func(_ context.Context, key uuid.UUID) Next {
		if key == slow {
			close(slowStarted)
			<-release
			return Next{Stop: true}
		}
		fastRuns.Add(1)
		return Next{Delay: time.Hour}
	})
	defer runner.Stop()
	defer close(release)

	runner.Wake(slow)
	<-slowStarted
	runner.Wake(fast)
	waitFor(t, func() bool { return fastRuns.Load() == 1 })
}

func TestStopEndsAWorkerAndAllowsRestart(t *testing.T) {
	var runs atomic.Int32
	runner := NewRunner(func(context.Context, uuid.UUID) Next {
		if runs.Add(1) == 1 {
			return Next{Stop: true}
		}
		return Next{Delay: time.Hour}
	})
	defer runner.Stop()

	key := uuid.New()
	runner.Wake(key)
	waitFor(t, func() bool {
		runner.mu.Lock()
		defer runner.mu.Unlock()
		return len(runner.workers) == 0
	})

	runner.Wake(key)
	waitFor(t, func() bool { return runs.Load() == 2 })
}

func TestRunnerStopWaitsForWorkInProgress(t *testing.T) {
	started := make(chan struct{})
	var finished atomic.Bool
	var once sync.Once
	runner := NewRunner(func(ctx context.Context, _ uuid.UUID) Next {
		once.Do(func() { close(started) })
		<-ctx.Done()
		time.Sleep(20 * time.Millisecond)
		finished.Store(true)
		return Next{Stop: true}
	})

	runner.Wake(uuid.New())
	<-started
	runner.Stop()
	if !finished.Load() {
		t.Fatal("Stop must wait for the work in progress")
	}
	runner.Wake(uuid.New()) // must be a no-op after Stop
}
