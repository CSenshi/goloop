//go:build darwin

package goloop

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// poller wraps a kqueue fd and a wakeup pipe.
// libuv: loop->backend_fd + loop->async_io_watcher (include/uv.h)
type poller struct {
	kqfd int            // libuv: loop->backend_fd (src/unix/kqueue.c: uv__platform_loop_init)
	rfd  int            // wakeup pipe read end (libuv: src/unix/async.c: uv__async_start)
	wfd  int            // wakeup pipe write end (libuv: src/unix/async.c: uv__async_send)
	cbs  map[int]func() // fd → callback
}

// newPoller creates the kqueue fd and wakeup pipe, registers rfd with kqueue.
// libuv: uv__platform_loop_init (src/unix/kqueue.c) + uv__async_start (src/unix/async.c)
func newPoller() (*poller, error) {
	kqfd, err := unix.Kqueue()
	if err != nil {
		return nil, fmt.Errorf("kqueue: %w", err)
	}

	var pipeFds [2]int
	if err := unix.Pipe(pipeFds[:]); err != nil {
		unix.Close(kqfd)
		return nil, fmt.Errorf("pipe: %w", err)
	}

	p := &poller{
		kqfd: kqfd,
		rfd:  pipeFds[0],
		wfd:  pipeFds[1],
		cbs:  make(map[int]func()),
	}

	if err := unix.SetNonblock(p.rfd, true); err != nil {
		p.close()
		return nil, fmt.Errorf("setnonblock rfd: %w", err)
	}

	// Register rfd so trigger() can break a blocked wait().
	ev := unix.Kevent_t{
		Ident:  uint64(p.rfd),
		Filter: unix.EVFILT_READ,
		Flags:  unix.EV_ADD | unix.EV_ENABLE,
	}
	if _, err := unix.Kevent(p.kqfd, []unix.Kevent_t{ev}, nil, nil); err != nil {
		p.close()
		return nil, fmt.Errorf("kevent add wakeup pipe: %w", err)
	}

	return p, nil
}

// add registers fd with kqueue; cb is called by wait() when fd becomes readable.
// libuv: uv__io_start (src/unix/core.c) + EV_ADD in uv__io_poll (src/unix/kqueue.c)
func (p *poller) add(fd int, cb func()) error {
	p.cbs[fd] = cb

	ev := unix.Kevent_t{
		Ident:  uint64(fd),
		Filter: unix.EVFILT_READ,
		Flags:  unix.EV_ADD | unix.EV_ENABLE,
	}
	if _, err := unix.Kevent(p.kqfd, []unix.Kevent_t{ev}, nil, nil); err != nil {
		delete(p.cbs, fd)
		return fmt.Errorf("kevent add fd %d: %w", fd, err)
	}
	return nil
}

// remove unregisters fd from kqueue.
// libuv: uv__io_stop (src/unix/core.c) + EV_DELETE in uv__io_poll (src/unix/kqueue.c)
func (p *poller) remove(fd int) error {
	ev := unix.Kevent_t{
		Ident:  uint64(fd),
		Filter: unix.EVFILT_READ,
		Flags:  unix.EV_DELETE,
	}
	if _, err := unix.Kevent(p.kqfd, []unix.Kevent_t{ev}, nil, nil); err != nil {
		return fmt.Errorf("kevent del fd %d: %w", fd, err)
	}
	delete(p.cbs, fd)
	return nil
}

// wait blocks until an fd is ready or timeoutMs expires (-1 = forever),
// then fires the registered callbacks. Wakeup pipe drains and returns early.
// libuv: uv__io_poll (src/unix/kqueue.c)
func (p *poller) wait(timeoutMs int) error {
	events := make([]unix.Kevent_t, 64)

	var ts *unix.Timespec
	if timeoutMs >= 0 {
		t := unix.NsecToTimespec(int64(timeoutMs) * int64(1e6))
		ts = &t
	}

	n, err := unix.Kevent(p.kqfd, nil, events[:], ts)
	if err != nil {
		return fmt.Errorf("kevent wait: %w", err)
	}

	for i := range n {
		fd := int(events[i].Ident)

		// Case 1: wakeup pipe is readable → drain it and return immediately.
		if fd == p.rfd {
			var buf [128]byte
			// Caveat: need while true loop to drain the pipe, as multiple triggers may have occurred
			for {
				nr, err := unix.Read(p.rfd, buf[:])
				if nr == 0 || err != nil {
					break
				}
			}
			return nil
		}

		// Case 2: some other fd is readable → call its callback.
		cb := p.cbs[fd]
		if cb != nil {
			cb()
		}
	}
	return nil
}

// trigger wakes a blocked wait() from any goroutine.
// libuv: uv_async_send → uv__async_send (src/unix/async.c)
func (p *poller) trigger() error {
	_, err := unix.Write(p.wfd, []byte{1})
	if err != nil {
		return fmt.Errorf("trigger write: %w", err)
	}
	return nil
}

// close releases the kqueue fd and wakeup pipe.
// libuv: uv__platform_loop_delete (src/unix/kqueue.c) + uv__async_stop (src/unix/async.c)
func (p *poller) close() error {
	unix.Close(p.kqfd)
	unix.Close(p.rfd)
	unix.Close(p.wfd)
	return nil
}
