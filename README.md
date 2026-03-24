# goloop

A Go implementation of [libuv](https://libuv.org/)'s core — a single-threaded event loop backed by epoll.

## What it is

goloop is an event loop library. It provides a way to wait for I/O events, timers and thread pool completions without blocking a goroutine per connection.

Each loop tick runs three phases in order:

1. **Poll** — `epoll_wait` until the nearest timer deadline; fire I/O callbacks
2. **Timers** — pop expired entries from the min-heap; fire timer callbacks
3. **Work completions** — drain the completion channel; fire `done` callbacks on the loop goroutine

All callbacks run serially on the single goroutine that called `Run()`. No locks needed inside callbacks.

## API

```go
l := goloop.New()

// Timers
id := l.SetTimeout(500*time.Millisecond, func() { fmt.Println("once") })
id  = l.SetInterval(1*time.Second, func() { fmt.Println("tick") })
l.ClearTimer(id)

// TCP
l.ListenTCP(":8080", func(conn *goloop.Conn) { /* ... */ })

// Thread pool
l.QueueWork(
    func() (any, error) { return doBlockingWork() },
    func(result any, err error) { /* runs on loop goroutine */ },
)

l.Run()   // blocks; all callbacks fire here
l.Stop()  // safe from any goroutine
```
