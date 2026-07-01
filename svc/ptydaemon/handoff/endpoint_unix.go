//go:build linux || darwin

package handoff

import (
	"context"
	"errors"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// unixEndpoint lends a PTY master fd via SCM_RIGHTS. Zero-copy: once granted the
// client reads the master directly and the daemon stops reading. The daemon only
// drains (discards) while detached so the child never blocks on a full buffer.
type unixEndpoint struct {
	mu     sync.Mutex
	master *os.File
}

func newUnixEndpoint(master *os.File) *unixEndpoint { return &unixEndpoint{master: master} }

// Drain reads-and-discards from the master until ctx is cancelled (the park
// signal) or the master goes away. Cancellation is delivered via a self-pipe so
// the blocking read is interruptible without closing the master fd.
func (e *unixEndpoint) Drain(ctx context.Context) error {
	e.mu.Lock()
	m := e.master
	e.mu.Unlock()
	if m == nil {
		return errors.New("handoff: no master")
	}
	mfd := int(m.Fd())

	r, w, err := os.Pipe()
	if err != nil {
		return err
	}
	defer r.Close()
	defer w.Close()
	// Wakeup goroutine: blocks on ctx until the Session parks (cancels) this Drain.
	// NOTE (bounded leak): if Drain returns early on its own (master POLLHUP/EIO —
	// child exited) this goroutine stays parked on <-ctx.Done() until the ctx is
	// later cancelled by the next parkDrainLocked/Close. In the daemon the session
	// is torn down on child exit, so it is released promptly; worst case is one
	// idle goroutine per not-yet-cancelled early-exit Drain. Threading a second
	// done-signal here would remove even that; deferred as low-risk.
	go func() {
		<-ctx.Done()
		_, _ = w.Write([]byte{0})
	}()
	rfd := int(r.Fd())

	buf := make([]byte, 32*1024)
	for {
		fds := []unix.PollFd{
			{Fd: int32(mfd), Events: unix.POLLIN},
			{Fd: int32(rfd), Events: unix.POLLIN},
		}
		if _, perr := unix.Poll(fds, -1); perr != nil {
			if perr == unix.EINTR {
				continue
			}
			return perr
		}
		if fds[1].Revents != 0 { // cancelled → parked (happens-before: see Session)
			return ctx.Err()
		}
		if fds[0].Revents&unix.POLLIN != 0 {
			n, rerr := unix.Read(mfd, buf)
			if n > 0 {
				continue // discard
			}
			if rerr == unix.EINTR || rerr == unix.EAGAIN {
				continue
			}
			return rerr // EOF/EIO → master gone
		}
		if fds[0].Revents&(unix.POLLHUP|unix.POLLERR|unix.POLLNVAL) != 0 {
			return nil
		}
	}
}

// Grant dups the master so the client receives an independent fd referring to
// the same PTY. The dup is owned by the returned ref until the sender transmits
// (and closes) it.
func (e *unixEndpoint) Grant() (HandoffRef, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.master == nil {
		return nil, errors.New("handoff: no master")
	}
	nfd, err := unix.Dup(int(e.master.Fd()))
	if err != nil {
		return nil, err
	}
	return &scmRef{fd: nfd}, nil
}

// Revoke is a no-op for the fd model: the daemon kept its own master fd, so the
// caller (Session) reclaims simply by restarting Drain. Escalation (closing the
// client control conn / signalling its PID) is a daemon-level concern.
func (e *unixEndpoint) Revoke(context.Context) error { return nil }

func (e *unixEndpoint) Resize(s Winsize) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.master == nil {
		return errors.New("handoff: no master")
	}
	return unix.IoctlSetWinsize(int(e.master.Fd()), unix.TIOCSWINSZ, &unix.Winsize{
		Row: s.Rows,
		Col: s.Cols,
	})
}

func (e *unixEndpoint) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.master == nil {
		return nil
	}
	err := e.master.Close()
	e.master = nil
	return err
}

// scmRef carries a dup'd master fd to be passed to the client via SCM_RIGHTS.
type scmRef struct{ fd int }

func (r *scmRef) Transport() Transport { return TransportSCMFD }
