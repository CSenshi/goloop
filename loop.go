package goloop

import (
	"container/heap"
	"sync/atomic"
	"time"
)

// EventLoop is the main event loop. Create with New(), start with Run().
type EventLoop struct {
	p           *poller
	stopped     atomic.Bool
	timers      timerHeap
	timerMap    map[TimerID]*timerEntry
	nextID      atomic.Uint64
	completions chan completion
}

// TimerID identifies a pending timer. Use with ClearTimer.
type TimerID uint64

// timerEntry is one node in the timer heap.
type timerEntry struct {
	id        TimerID
	deadline  time.Time
	fn        func()
	interval  time.Duration // 0 = one-shot (SetTimeout)
	cancelled bool
}

// timerHeap is a min-heap of *timerEntry ordered by deadline.
type timerHeap []*timerEntry

func (h timerHeap) Len() int           { return len(h) }
func (h timerHeap) Less(i, j int) bool { return h[i].deadline.Before(h[j].deadline) }
func (h timerHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *timerHeap) Push(x any)        { *h = append(*h, x.(*timerEntry)) }
func (h *timerHeap) Pop() any {
	old := *h
	n := len(old)
	e := old[n-1]
	old[n-1] = nil
	*h = old[:n-1]
	return e
}

// completion carries the result of a QueueWork job back to the loop goroutine.
type completion struct {
	result any
	err    error
	done   func(any, error)
}

// New creates a new event loop. Does not start it.
// libuv: uv_loop_init(uv_loop_t*)
func New() *EventLoop {
	p, err := newPoller()
	if err != nil {
		panic("goloop: newPoller: " + err.Error())
	}
	l := &EventLoop{
		p:           p,
		timerMap:    make(map[TimerID]*timerEntry),
		completions: make(chan completion, 512),
	}
	heap.Init(&l.timers)
	return l
}

// Run starts the event loop. Blocks until Stop() is called or a fatal error occurs.
// All callbacks fire on the goroutine that called Run().
// libuv: uv_run(uv_loop_t*, UV_RUN_DEFAULT)
func (l *EventLoop) Run() error {
	for !l.stopped.Load() {
		// --- Phase 1: POLL ---
		// Block until an fd is ready or the nearest timer deadline arrives.
		if err := l.p.wait(l.pollTimeout()); err != nil {
			return err
		}
		if l.stopped.Load() {
			break
		}

		// --- Phase 2: TIMERS ---
		now := time.Now()
		for l.timers.Len() > 0 {
			top := l.timers[0]
			if top.deadline.After(now) {
				break
			}
			heap.Pop(&l.timers)
			if top.cancelled {
				delete(l.timerMap, top.id)
				continue
			}
			if top.interval > 0 {
				// Re-arm before calling fn so ClearTimer inside fn works.
				top.deadline = top.deadline.Add(top.interval)
				heap.Push(&l.timers, top)
			} else {
				delete(l.timerMap, top.id)
			}
			top.fn()
		}

		// --- Phase 3: WORK COMPLETIONS ---
		for {
			select {
			case c := <-l.completions:
				c.done(c.result, c.err)
			default:
				goto doneCompletions
			}
		}
	doneCompletions:
	}
	return l.p.close()
}

// pollTimeout returns the milliseconds until the nearest pending timer deadline,
// or -1 if no timers are set (meaning: block forever until an fd fires).
func (l *EventLoop) pollTimeout() int {
	for l.timers.Len() > 0 {
		top := l.timers[0]
		if top.cancelled {
			heap.Pop(&l.timers)
			delete(l.timerMap, top.id)
			continue
		}
		d := time.Until(top.deadline)
		if d <= 0 {
			return 0
		}
		ms := int(d.Milliseconds())
		if ms == 0 {
			ms = 1 // ensure at least 1ms so we don't busy-loop on sub-ms deadlines
		}
		return ms
	}
	return -1
}

// Stop signals the loop to exit after the current tick completes.
// Safe to call from any goroutine.
// libuv: uv_stop(uv_loop_t*)
func (l *EventLoop) Stop() {
	l.stopped.Store(true)
	l.p.trigger() //nolint:errcheck — best-effort wakeup
}

// SetTimeout fires fn once after duration d.
// libuv: uv_timer_init + uv_timer_start (repeat=0)
func (l *EventLoop) SetTimeout(d time.Duration, fn func()) TimerID {
	return l.addTimer(d, fn, 0)
}

// SetInterval fires fn repeatedly every duration d.
// libuv: uv_timer_init + uv_timer_start (repeat=d)
func (l *EventLoop) SetInterval(d time.Duration, fn func()) TimerID {
	return l.addTimer(d, fn, d)
}

func (l *EventLoop) addTimer(d time.Duration, fn func(), interval time.Duration) TimerID {
	id := TimerID(l.nextID.Add(1))
	e := &timerEntry{
		id:       id,
		deadline: time.Now().Add(d),
		fn:       fn,
		interval: interval,
	}
	l.timerMap[id] = e
	heap.Push(&l.timers, e)
	return id
}

// ClearTimer cancels a pending timer. No-op if already fired.
// Safe to call from within a callback.
// libuv: uv_timer_stop(uv_timer_t*)
func (l *EventLoop) ClearTimer(id TimerID) {
	if e, ok := l.timerMap[id]; ok {
		e.cancelled = true
		// Leave the entry in the heap; it will be skipped at pop time.
		// The timerMap entry is cleaned up when the entry is eventually popped.
	}
}

// ListenTCP binds to addr and calls onConn for each accepted connection.
// onConn fires on the loop goroutine.
// libuv: uv_tcp_init + uv_tcp_bind + uv_listen(uv_stream_t*, backlog, cb)
func (l *EventLoop) ListenTCP(addr string, onConn func(*Conn)) error {
	panic("not implemented") // phase 4
}

// QueueWork sends fn to the thread pool. done fires on the loop goroutine when fn completes.
// libuv: uv_queue_work(uv_loop_t*, uv_work_t*, work_cb, after_work_cb)
func (l *EventLoop) QueueWork(fn func() (any, error), done func(any, error)) {
	go func() {
		result, err := fn()
		l.completions <- completion{result, err, done}
		l.p.trigger()
	}()
}
