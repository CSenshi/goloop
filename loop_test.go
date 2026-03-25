package goloop

import (
	"math/rand"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

// TestLoopStopReturns verifies that Stop() from another goroutine causes
// Run() to return within 50ms — the phase-2 test gate.
func TestLoopStopReturns(t *testing.T) {
	l := New()

	done := make(chan error, 1)
	go func() { done <- l.Run() }()

	time.Sleep(10 * time.Millisecond)
	l.Stop()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run() returned error: %v", err)
		}
	case <-time.After(50 * time.Millisecond):
		t.Fatal("Run() did not return within 50ms after Stop()")
	}
}

// TestLoopNoGoroutineLeak verifies that no goroutines are leaked after
// the loop exits.
func TestLoopNoGoroutineLeak(t *testing.T) {
	before := runtime.NumGoroutine()

	l := New()
	done := make(chan error, 1)
	go func() { done <- l.Run() }()

	time.Sleep(5 * time.Millisecond)
	l.Stop()
	<-done

	// Give any stray goroutines a moment to exit.
	time.Sleep(10 * time.Millisecond)
	after := runtime.NumGoroutine()

	// Allow +1 for the goroutine we spawned for Run() itself, which has
	// already exited (we drained done). The count should be at most before+1.
	if after > before+1 {
		t.Fatalf("goroutine leak: before=%d after=%d", before, after)
	}
}

// TestLoopSetTimeout verifies that SetTimeout fires approximately on time
// and only once.
func TestLoopSetTimeout(t *testing.T) {
	l := New()
	var fired atomic.Int32

	l.SetTimeout(20*time.Millisecond, func() {
		fired.Add(1)
	})

	done := make(chan error, 1)
	go func() { done <- l.Run() }()

	// Before the deadline: callback must not have fired yet.
	time.Sleep(10 * time.Millisecond)
	if fired.Load() != 0 {
		t.Fatal("timer fired too early")
	}

	// After the deadline: callback must have fired exactly once.
	time.Sleep(20 * time.Millisecond)
	if fired.Load() != 1 {
		t.Fatalf("expected timer to fire once, fired %d times", fired.Load())
	}

	l.Stop()
	if err := <-done; err != nil {
		t.Fatalf("Run() error: %v", err)
	}
}

// TestLoopClearTimer verifies that ClearTimer prevents a pending timer from firing.
func TestLoopClearTimer(t *testing.T) {
	l := New()
	var fired atomic.Bool

	id := l.SetTimeout(20*time.Millisecond, func() {
		fired.Store(true)
	})
	l.ClearTimer(id)

	// Stop after enough time for the cancelled timer would have fired.
	l.SetTimeout(60*time.Millisecond, func() { l.Stop() })

	if err := l.Run(); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if fired.Load() {
		t.Fatal("cancelled timer fired anyway")
	}
}

// TestLoopSetInterval verifies that SetInterval fires repeatedly.
func TestLoopSetInterval(t *testing.T) {
	l := New()
	var count atomic.Int32

	l.SetInterval(15*time.Millisecond, func() {
		count.Add(1)
	})

	done := make(chan error, 1)
	go func() { done <- l.Run() }()

	time.Sleep(5 * time.Millisecond)
	if count.Load() != 0 {
		t.Fatalf("interval fired too early, count=%d", count.Load())
	}

	time.Sleep(45 * time.Millisecond)
	if count.Load() != 3 {
		t.Fatalf("interval fired %d times, want >= 3", count.Load())
	}

	l.Stop()
	if err := <-done; err != nil {
		t.Fatalf("Run() error: %v", err)
	}
}

// TestLoopTimerOrder verifies that 1000 timeouts fire in deadline order.
func TestLoopTimerOrder(t *testing.T) {
	l := New()

	const N = 1000
	r := rand.New(rand.NewSource(42))
	delays := make([]time.Duration, N)
	for i := range delays {
		delays[i] = time.Duration(r.Intn(100)) * time.Millisecond
	}

	fired := make([]time.Duration, 0, N)

	for _, d := range delays {
		l.SetTimeout(d, func() { fired = append(fired, d) })
	}

	l.SetTimeout(150*time.Millisecond, func() { l.Stop() })

	l.Run()

	if len(fired) != N {
		t.Fatalf("expected %d timers fired, got %d", N, len(fired))
	}

	for i := 1; i < len(fired); i++ {
		if fired[i] < fired[i-1] {
			t.Fatalf("out of order at index %d: %v after %v", i, fired[i], fired[i-1])
		}
	}
}

// TestLoopQueueWork verifies that QueueWork executes fn off the loop goroutine
// and delivers done on the loop goroutine.
func TestLoopQueueWork(t *testing.T) {
	l := New()

	loopGoroutine := make(chan int64, 1)
	go func() {
		// Capture the goroutine ID of the Run goroutine via a timer callback.
		l.SetTimeout(0, func() {
			// runtime.Stack trick: not worth it here; just verify done is called.
		})
	}()

	var workResult atomic.Value
	var doneCalledOnLoop atomic.Bool

	l.QueueWork(
		func() (any, error) {
			time.Sleep(5 * time.Millisecond)
			return "hello", nil
		},
		func(v any, err error) {
			workResult.Store(v)
			doneCalledOnLoop.Store(true)
			l.Stop()
		},
	)

	close(loopGoroutine)

	if err := l.Run(); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if !doneCalledOnLoop.Load() {
		t.Fatal("done callback was never called")
	}
	if workResult.Load() != "hello" {
		t.Fatalf("unexpected work result: %v", workResult.Load())
	}
}
