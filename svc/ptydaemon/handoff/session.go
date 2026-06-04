package handoff

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
)

// Session ties an Endpoint to a Lease and a HandoffSender. It owns the drain
// goroutine lifecycle and enforces park-before-grant. It is platform-agnostic:
// all OS mechanism lives behind ep.
type Session struct {
	ep     Endpoint
	lease  *Lease
	sender HandoffSender
	nextID atomic.Uint64

	mu          sync.Mutex
	drainCancel context.CancelFunc
	drainDone   chan struct{}
}

// NewSession builds a session over ep, sending handoff refs via sender.
func NewSession(ep Endpoint, sender HandoffSender) *Session {
	return &Session{ep: ep, lease: NewLease(), sender: sender}
}

// canceledCtx returns an already-cancelled context. Used on the Attach cleanup
// paths where the lease was committed but no borrower exists to ack a yield, so the
// revoke must return immediately instead of waiting for an ack that never comes.
func canceledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// Start begins draining (daemon owns the read side until a client attaches).
func (s *Session) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.drainCancel == nil {
		s.startDrainLocked()
	}
}

func (s *Session) startDrainLocked() {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s.drainCancel = cancel
	s.drainDone = done
	go func() {
		defer close(done)
		_ = s.ep.Drain(ctx)
	}()
}

// parkDrainLocked cancels the drainer and BLOCKS until it has returned. The
// channel receive is the happens-before edge guaranteeing the drainer's last
// read completes-before the client's first read (plan §14.3).
func (s *Session) parkDrainLocked() {
	if s.drainCancel != nil {
		s.drainCancel()
		<-s.drainDone
		s.drainCancel = nil
		s.drainDone = nil
	}
}

// Attach parks the drainer, grants the lease to a new client, and sends the
// handoff ref over conn. Returns the revoke signal the caller's control reader
// must watch; on revoke it should stop the client and call AckYield.
func (s *Session) Attach(conn net.Conn) (revoke <-chan struct{}, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.parkDrainLocked() // no torn reads: daemon stops before the client starts

	id := s.nextID.Add(1)
	revoke, err = s.lease.grantTo(id)
	if err != nil {
		// Do NOT restart the drainer here. grantTo only fails with ErrLeaseGranted,
		// which means a previous client (or a stale grant) still logically owns the
		// read side — the drainer is already parked. Spawning a drainer now would
		// create a SECOND reader racing the lease holder's dup'd fd (torn output).
		// A caller that finds the holder abandoned without cleanup must Steal first;
		// the buffer-full-deadlock case belongs in a daemon-level control-conn
		// watchdog, not here.
		return nil, err
	}

	ref, err := s.ep.Grant()
	if err != nil {
		// Grant failed AFTER grantTo committed the lease (e.g. Windows H4 PID-verify
		// rejected the relay client). There is no borrower to ack a yield, so revoke
		// with an already-cancelled ctx: revokeLease must not block waiting for an
		// ack that will never come.
		s.lease.revokeLease(canceledCtx())
		s.startDrainLocked()
		return nil, err
	}
	if err := s.sender.Send(conn, ref); err != nil {
		s.lease.revokeLease(canceledCtx())
		s.startDrainLocked()
		return nil, err
	}
	return revoke, nil
}

// AckYield records that the borrower stopped reading. Safe to call from the
// control-reader goroutine without the Session lock (see Lease.clientYielded).
func (s *Session) AckYield() { s.lease.clientYielded() }

// Steal forcibly reclaims the read side from the client. ctx bounds the wait for
// a cooperative yield before the caller escalates (force disconnect / signal).
func (s *Session) Steal(ctx context.Context) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lease.revokeLease(ctx)
	_ = s.ep.Revoke(ctx)
	if s.drainCancel == nil {
		s.startDrainLocked()
	}
}

// Detach handles a client going away (clean exit or EOF). It acks the yield,
// reclaims the lease, and resumes draining.
func (s *Session) Detach() {
	s.lease.clientYielded()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lease.revokeLease(context.Background())
	_ = s.ep.Revoke(context.Background())
	if s.drainCancel == nil {
		s.startDrainLocked()
	}
}

// Resize forwards a size change to the endpoint; independent of the lease.
func (s *Session) Resize(size Winsize) error { return s.ep.Resize(size) }

// Owner reports whether the daemon currently owns the read side.
func (s *Session) Owner() bool { return s.lease.Owner() }

// Close parks the drainer and closes the endpoint.
func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.parkDrainLocked()
	return s.ep.Close()
}
