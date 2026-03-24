//go:build darwin

package goloop

import (
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestPollerPipeReadable: register a pipe's read-end; write from a goroutine;
// assert wait() fires the callback.
func TestPollerPipeReadable(t *testing.T) {
	p, err := newPoller()
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()

	var pipeFds [2]int
	if err := unix.Pipe(pipeFds[:]); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(pipeFds[0])
	defer unix.Close(pipeFds[1])

	fired := make(chan struct{}, 1)
	if err := p.add(pipeFds[0], func() { fired <- struct{}{} }); err != nil {
		t.Fatal(err)
	}

	// Write a byte from a separate goroutine after a short delay.
	go func() {
		time.Sleep(10 * time.Millisecond)
		unix.Write(pipeFds[1], []byte{1})
	}()

	// wait() should return once the pipe is readable.
	if err := p.wait(200); err != nil {
		t.Fatal(err)
	}

	select {
	case <-fired:
		// success
	default:
		t.Fatal("callback was not called")
	}
}

// TestPollerTriggerWakesWait: trigger() must wake a blocking wait() immediately.
func TestPollerTriggerWakesWait(t *testing.T) {
	p, err := newPoller()
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()

	done := make(chan error, 1)
	go func() {
		// Block with a 5-second timeout; trigger() should wake it early.
		done <- p.wait(5000)
	}()

	time.Sleep(10 * time.Millisecond)
	if err := p.trigger(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("wait() did not return after trigger()")
	}
}

// TestPollerMultipleFdsAndTrigger simulates three loop ticks with 5 connections:
//
//	tick 1: conns 1, 3, 4 send data  → callbacks fire in some order, then wait() returns
//	tick 2: background worker calls trigger() → wait() returns early, no callbacks
//	tick 3: conns 0, 2 send data     → only those two callbacks fire
//
// Verifies correct fd selection across ticks and that trigger() fires no callbacks.
func TestPollerMultipleFdsAndTrigger(t *testing.T) {
	p, err := newPoller()
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()

	const n = 5
	var pipes [n][2]int
	for i := range pipes {
		if err := unix.Pipe(pipes[i][:]); err != nil {
			t.Fatal(err)
		}
		defer unix.Close(pipes[i][0])
		defer unix.Close(pipes[i][1])
	}

	// order records the sequence of callback indices across all ticks.
	// Callbacks drain the pipe so kqueue doesn't re-report it next tick,
	// matching what a real read handler would do.
	var order []int
	var buf [64]byte
	for i := range pipes {
		if err := p.add(pipes[i][0], func() {
			unix.Read(pipes[i][0], buf[:])
			order = append(order, i)
		}); err != nil {
			t.Fatal(err)
		}
	}

	// --- tick 1: conns 1, 3, 4 become readable ---
	unix.Write(pipes[1][1], []byte{1})
	unix.Write(pipes[3][1], []byte{1})
	unix.Write(pipes[4][1], []byte{1})

	if err := p.wait(200); err != nil {
		t.Fatal(err)
	}

	// kqueue doesn't guarantee intra-batch order, so check the set not sequence.
	tick1 := map[int]bool{1: true, 3: true, 4: true}
	if len(order) != 3 {
		t.Fatalf("tick1: expected 3 callbacks, got %d (order=%v)", len(order), order)
	}
	for _, idx := range order {
		if !tick1[idx] {
			t.Fatalf("tick1: unexpected callback %d fired (order=%v)", idx, order)
		}
	}

	// --- tick 2: background worker finishes, trigger() wakes wait() with no I/O ---
	orderBefore := len(order)
	go func() {
		time.Sleep(10 * time.Millisecond)
		p.trigger()
	}()

	done := make(chan error, 1)
	go func() { done <- p.wait(5000) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("tick2: wait() did not return after trigger()")
	}
	if len(order) != orderBefore {
		t.Fatalf("tick2: trigger() must fire no callbacks, got %v", order[orderBefore:])
	}

	// --- tick 3: conns 0 and 2 become readable ---
	unix.Write(pipes[0][1], []byte{1})
	unix.Write(pipes[2][1], []byte{1})

	if err := p.wait(200); err != nil {
		t.Fatal(err)
	}

	tick3 := order[3:]
	if len(tick3) != 2 {
		t.Fatalf("tick3: expected 2 callbacks, got %d (order=%v)", len(tick3), order)
	}
	tick3Set := map[int]bool{0: true, 2: true}
	for _, idx := range tick3 {
		if !tick3Set[idx] {
			t.Fatalf("tick3: unexpected callback %d fired (order=%v)", idx, order)
		}
	}
}

// TestPollerFileFdReadable: register a regular file fd; kqueue reports it
// readable immediately because bytes are available from offset 0.
// Exercises a non-pipe fd type — closer to how a TCP socket fires on arrival.
func TestPollerFileFdReadable(t *testing.T) {
	p, err := newPoller()
	if err != nil {
		t.Fatal(err)
	}
	defer p.close()

	f, err := os.CreateTemp("", "goloop-poller-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	defer f.Close()

	if _, err := f.WriteString("hello"); err != nil {
		t.Fatal(err)
	}
	// Seek back so kqueue sees bytes available from the current offset.
	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}

	fired := false
	if err := p.add(int(f.Fd()), func() { fired = true }); err != nil {
		t.Fatal(err)
	}

	// File has data at offset 0 — kqueue should report it readable immediately.
	if err := p.wait(200); err != nil {
		t.Fatal(err)
	}

	if !fired {
		t.Fatal("callback was not called for file fd")
	}
}

// TestPollerNoFdLeak: close() must not leave the kqueue or wakeup pipe fds open.
func TestPollerNoFdLeak(t *testing.T) {
	p, err := newPoller()
	if err != nil {
		t.Fatal(err)
	}

	kqfd := p.kqfd
	rfd := p.rfd
	wfd := p.wfd

	if err := p.close(); err != nil {
		t.Fatal(err)
	}

	// After close, all fds should be invalid — any operation on them must fail.
	if _, err := unix.Kevent(kqfd, nil, make([]unix.Kevent_t, 1), nil); err == nil {
		t.Error("kqfd still open after close()")
	}
	var buf [8]byte
	if _, err := unix.Read(rfd, buf[:]); err == nil {
		t.Error("rfd still open after close()")
	}
	if _, err := unix.Write(wfd, buf[:]); err == nil {
		t.Error("wfd still open after close()")
	}
}
