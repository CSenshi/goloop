# goloop — Design Reference

> Internal technical reference. Written before code.
> Code that contradicts this document is wrong unless this document is updated first.

---

## What goloop is

A Go implementation of libuv's core. An event loop library - not a framework.
The caller controls the loop. The loop controls nothing about the caller's protocol or data model.

## What goloop is not

- Not Node.js. No microtask queue, no macrotask queue, no Promise queue. Those are JS runtime
  concepts layered on top of libuv. goloop is libuv, not Node.
- Not Go's `net` package. No goroutine per connection.
- Not cross-platform in v1. Linux only.

---

## Core mental model

> The event loop is a single goroutine that loops forever. On each iteration it asks the OS
> which fds are ready and which timers have expired, then calls the appropriate callbacks.
> It never blocks longer than the nearest timer deadline. All user callbacks run on this one
> goroutine, serially, never concurrently.

### The loop tick — three phases, in order, every iteration

```
1. POLL
   epoll_wait(timeout = ms until nearest timer deadline, or -1 if no timers)
   for each ready fd → call its registered callback

2. TIMERS
   now = time.Now()
   pop every entry from heap where deadline <= now
   call each callback
   if SetInterval: re-insert with next deadline

3. WORK COMPLETIONS
   drain completion channel (non-blocking)
   call each done callback on the loop goroutine
```

No other phases. No priority queues. No microtask checkpoints between callbacks.
This matches libuv's loop phases directly.

---

## Invariants

- **One loop goroutine.** `Run()` occupies exactly one goroutine for the loop's lifetime.
- **Callbacks are serial.** No two callbacks ever run concurrently.
- **No `net` package.** All fd operations use `golang.org/x/sys/unix` directly.
- **`golang.org/x/sys/unix` only.** Never import `syscall` — it is deprecated for low-level Linux.

### Caller rules

1. **Never block in a callback.** Use `QueueWork` for blocking operations.
2. **No locks needed inside callbacks.** Callbacks are serial on the loop goroutine.
3. **Timers are best-effort.** A slow callback delays subsequent timers. No hard latency guarantee.

---

## Design order vs implementation order

These are opposite directions. Both matter.

**Design order — top-down.**
Start at the highest abstraction. Define what the caller sees before defining what the
internals look like. Write stubs. The public API is the contract — it constrains every
decision below it.

```
loop.go       (public API — defined first, implemented last among core files)
timer.go      (depends on loop)
tcp.go        (depends on loop + poller)
workpool.go   (depends on loop + poller)
poller_linux.go (no dependencies — defined last in design, because upper layers define what it must provide)
```

**Implementation order — bottom-up.**
You cannot run anything without epoll. Tests must pass layer by layer.
Each layer is only implemented after the layer below it has passing tests.

```
poller_linux.go  → loop.go  → timer.go  → tcp.go  → workpool.go
```

**The workflow:**
1. Write all public API stubs now (design order).
2. Implement bottom-up (implementation order).
3. As each layer is implemented, fill in the corresponding section of this document.

---

## Public API — full surface, defined upfront

All stubs. Bodies filled in during implementation.

### `loop.go`

```go
package goloop

import "time"

type EventLoop struct {
    // fields added when implementation begins
}

type TimerID uint64

// New creates a new event loop. Does not start it.
func New() *EventLoop

// Run starts the event loop. Blocks until Stop() is called or a fatal error occurs.
// All callbacks fire on the goroutine that called Run().
func (l *EventLoop) Run() error

// Stop signals the loop to exit after the current tick completes.
// Safe to call from any goroutine.
func (l *EventLoop) Stop()

// SetTimeout fires fn once after duration d.
func (l *EventLoop) SetTimeout(d time.Duration, fn func()) TimerID

// SetInterval fires fn repeatedly every duration d.
func (l *EventLoop) SetInterval(d time.Duration, fn func()) TimerID

// ClearTimer cancels a pending timer. No-op if already fired.
// Safe to call from within a callback.
func (l *EventLoop) ClearTimer(id TimerID)

// ListenTCP binds to addr and calls onConn for each accepted connection.
// onConn fires on the loop goroutine.
func (l *EventLoop) ListenTCP(addr string, onConn func(*Conn)) error

// QueueWork sends fn to the thread pool. done fires on the loop goroutine when fn completes.
func (l *EventLoop) QueueWork(fn func() (any, error), done func(any, error))
```

---

## Implementation detail — filled in per phase

Each section below is empty until that phase's implementation begins.
When you start a phase: open this doc, fill in the section, then write the code.

---

## Phase 2 — loop.go

### EventLoop fields

```go
type EventLoop struct {
    p           *poller          // OS poller (kqueue on darwin, epoll on linux)
    stopped     atomic.Bool      // set by Stop(); read at the top of every tick
    timers      timerHeap        // min-heap ordered by deadline
    timerMap    map[TimerID]*timerEntry  // parallel index for O(1) ClearTimer
    nextID      atomic.Uint64    // monotonically increasing timer ID source
    completions chan completion   // work results sent here by QueueWork goroutines
}
```

**Why each field exists:**

- `p` — the loop must block on OS I/O readiness. All fd registration and polling goes through the poller so loop.go stays OS-agnostic.
- `stopped` — `Stop()` is documented as safe to call from any goroutine, so the flag must be atomic. A plain `bool` would be a data race.
- `timers` + `timerMap` — a min-heap gives O(log n) insert/pop and O(1) peek at the nearest deadline; the parallel map gives O(1) cancel. Without the map, `ClearTimer` would require an O(n) heap scan. Cancelled entries are skipped lazily at pop time rather than removed eagerly — safe because the heap's ordering property is preserved.
- `nextID` — timer IDs are handed to callers and may be cancelled from any goroutine, so the counter must be atomic.
- `completions` — `QueueWork` launches a goroutine for each job. The goroutine can't call user callbacks directly (they must run on the loop goroutine), so it sends results to this channel. The loop drains it in phase 3. Buffered at 512 so goroutines rarely block.

### Three-phase tick

```
POLL → TIMERS → WORK COMPLETIONS
```

**Why poll first?**

The loop blocks in `p.wait(timeoutMs)` during POLL. The timeout is set to the time remaining until the nearest pending timer, so the loop never oversleeps past a deadline. When the OS wakes the loop early (fd ready), we still run the TIMERS phase immediately after — any timers that expired during the poll are serviced in the same tick.

Polling first (not last) matches libuv. The rationale: I/O events arrive from the outside world and have no "due time"; they should be processed as soon as the OS reports them. Timers are internal; they are checked after the blocking phase ends.

**Why drain completions non-blocking?**

Phase 3 uses a `select { case c := <-completions: … default: break }` loop. If it blocked, a steady stream of QueueWork completions could starve the next POLL phase and delay I/O callbacks indefinitely. Non-blocking drain means: handle everything that arrived this tick, then go back to sleep.

### Stop / wakeup contract

`Stop()` does two things atomically in sequence:
1. `stopped.Store(true)` — ensures the loop sees the flag on the next iteration even if it wakes for another reason.
2. `p.trigger()` — writes one byte to the wakeup pipe so a sleeping `p.wait()` returns immediately.

This means Stop() returns quickly regardless of how long the current poll timeout is. The loop checks `stopped` immediately after `p.wait()` returns and exits before firing any callbacks.

### QueueWork wakes the poller

After sending to `completions`, each QueueWork goroutine calls `p.trigger()`. Without this, a completed job would wait until the next poll timeout expired before its `done` callback ran — up to the nearest timer deadline, or forever if no timers are set.

---

## Implementation order and test gates

| Order | File | Test gate before moving on |
|---|---|---|
| 1 | `poller_linux.go` | Register pipe fd. Write from goroutine. Assert `wait` fires. Assert `trigger` wakes sleeping `wait`. Assert no fd leak on `close`. |
| 2 | `loop.go` | Start loop. `Stop()` from goroutine after 10ms. `Run()` returns within 50ms. No goroutine leak. |
| 3 | `timer.go` | 1000 timeouts fire in deadline order. `ClearTimer` prevents fire. `SetInterval` fires N times in N×interval. |
| 4 | `tcp.go` | Echo server. 500 clients × 10 msgs. All echoes received. No fd leak after disconnect. |
| 5 | `workpool.go` | 1000 jobs × 1ms sleep. All done callbacks on loop goroutine. Pool bounded to N goroutines. |

---

## Out of scope for v1

- Microtask / macrotask queues (JS/Node concepts, not libuv)
- UDP, Unix domain sockets, TLS
- kqueue / Darwin, Windows / IOCP
- Multiple event loops
- Backpressure API
- Any application protocol

---

## Glossary

| Term | Meaning |
|---|---|
| loop goroutine | The single goroutine running `Run()` |
| tick | One iteration of the loop: poll → timers → completions |
| fd | File descriptor — OS handle for a socket, pipe, or eventfd |
| callback | Any `func()` supplied by the caller, fired by the loop |