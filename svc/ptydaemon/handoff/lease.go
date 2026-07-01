package handoff

import (
	"context"
	"errors"
	"sync"
)

// ErrLeaseGranted is returned when a grant is attempted while the lease is
// already held by a client.
var ErrLeaseGranted = errors.New("handoff: lease already granted")

type leaseState int

const (
	leaseDraining leaseState = iota // daemon reads
	leaseGranted                    // one client reads
)

// Lease arbitrates the single-reader invariant with ASYMMETRIC authority.
//
// Note what is and isn't exported: the daemon-side Session calls grantTo /
// revokeLease; a borrower is only ever handed a receive-only <-chan struct{}
// (the revoke signal) and may ack via clientYielded. There is deliberately NO
// method that lets a client move the state to Granted or block a Revoke. The
// client cannot steal because the type gives it no verb to do so.
type Lease struct {
	mu     sync.Mutex
	state  leaseState
	holder uint64        // client id, 0 == daemon
	revoke chan struct{} // closed by the daemon to demand the holder yield
	yield  chan struct{} // closed by the borrower (via Session.AckYield) to ack it stopped
}

// NewLease returns a lease owned by the daemon (draining).
func NewLease() *Lease {
	return &Lease{state: leaseDraining}
}

// Owner reports whether the daemon currently owns the read side (draining).
func (l *Lease) Owner() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.state == leaseDraining
}

// grantTo (daemon-only) flips Draining→Granted and returns the revoke signal the
// borrower must watch. The caller MUST have parked the daemon reader first.
func (l *Lease) grantTo(id uint64) (revoke <-chan struct{}, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.state != leaseDraining {
		return nil, ErrLeaseGranted
	}
	l.state, l.holder = leaseGranted, id
	l.revoke = make(chan struct{})
	l.yield = make(chan struct{})
	return l.revoke, nil
}

// clientYielded (ack path) records that the borrower stopped reading. It takes
// ONLY l.mu — never the Session lock — so it can unblock a revokeLease that is
// waiting on the ack even while the Session lock is held by the stealer.
func (l *Lease) clientYielded() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.yield != nil {
		select {
		case <-l.yield:
		default:
			close(l.yield)
		}
	}
}

// revokeLease (daemon-only) preempts the borrower: signal, wait for ack or the
// ctx deadline, then flip back to Draining. Never unbounded.
//
// Idempotency is SCOPED to Session's serialization: grantTo and revokeLease are
// only ever called under Session.mu, so they never overlap. The lock is dropped
// between the two sections here (to wait on yield without holding l.mu), leaving a
// window where state==leaseGranted but l.revoke is already closed — safe because
// Owner() reports false in it and no concurrent grantTo/revokeLease can run. Two
// concurrent BARE revokeLease calls on the same Lease (outside Session) would both
// reset state and are NOT supported; the asymmetric-authority model forbids them.
func (l *Lease) revokeLease(ctx context.Context) {
	l.mu.Lock()
	if l.state != leaseGranted {
		l.mu.Unlock()
		return
	}
	if l.revoke != nil {
		select {
		case <-l.revoke:
		default:
			close(l.revoke)
		}
	}
	yield := l.yield
	l.mu.Unlock()

	if yield != nil {
		select {
		case <-yield: // borrower acked it stopped reading
		case <-ctx.Done(): // deadline → caller escalates (force disconnect / signal)
		}
	}

	l.mu.Lock()
	l.state, l.holder = leaseDraining, 0
	l.revoke, l.yield = nil, nil
	l.mu.Unlock()
}
