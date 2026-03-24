package goloop

import "time"

// EventLoop is the main event loop. Create with New(), start with Run().
type EventLoop struct {
	// fields added during implementation
}

// TimerID identifies a pending timer. Use with ClearTimer.
type TimerID uint64

// New creates a new event loop. Does not start it.
// libuv: uv_loop_init(uv_loop_t*)
func New() *EventLoop { panic("not implemented") }

// Run starts the event loop. Blocks until Stop() is called or a fatal error occurs.
// All callbacks fire on the goroutine that called Run().
// libuv: uv_run(uv_loop_t*, UV_RUN_DEFAULT)
func (l *EventLoop) Run() error { panic("not implemented") }

// Stop signals the loop to exit after the current tick completes.
// Safe to call from any goroutine.
// libuv: uv_stop(uv_loop_t*)
func (l *EventLoop) Stop() { panic("not implemented") }

// SetTimeout fires fn once after duration d.
// libuv: uv_timer_init + uv_timer_start (repeat=0)
func (l *EventLoop) SetTimeout(d time.Duration, fn func()) TimerID {
	panic("not implemented")
}

// SetInterval fires fn repeatedly every duration d.
// libuv: uv_timer_init + uv_timer_start (repeat=d)
func (l *EventLoop) SetInterval(d time.Duration, fn func()) TimerID {
	panic("not implemented")
}

// ClearTimer cancels a pending timer. No-op if already fired.
// Safe to call from within a callback.
// libuv: uv_timer_stop(uv_timer_t*)
func (l *EventLoop) ClearTimer(id TimerID) { panic("not implemented") }

// ListenTCP binds to addr and calls onConn for each accepted connection.
// onConn fires on the loop goroutine.
// libuv: uv_tcp_init + uv_tcp_bind + uv_listen(uv_stream_t*, backlog, cb)
func (l *EventLoop) ListenTCP(addr string, onConn func(*Conn)) error {
	panic("not implemented")
}

// QueueWork sends fn to the thread pool. done fires on the loop goroutine when fn completes.
// libuv: uv_queue_work(uv_loop_t*, uv_work_t*, work_cb, after_work_cb)
func (l *EventLoop) QueueWork(fn func() (any, error), done func(any, error)) {
	panic("not implemented")
}
