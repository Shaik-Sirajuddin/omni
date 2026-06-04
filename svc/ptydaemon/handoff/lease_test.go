package handoff

import (
	"context"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestLeaseAsymmetry(t *testing.T) {
	l := NewLease()
	if !l.Owner() {
		t.Fatal("new lease must be daemon-owned (draining)")
	}
	revoke, err := l.grantTo(1)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	if l.Owner() {
		t.Fatal("granted lease must not be daemon-owned")
	}
	if _, err := l.grantTo(2); err != ErrLeaseGranted {
		t.Fatalf("double grant must return ErrLeaseGranted, got %v", err)
	}

	// Borrower watches revoke and acks via clientYielded.
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-revoke
		l.clientYielded()
	}()
	l.revokeLease(context.Background())
	wg.Wait()

	if !l.Owner() {
		t.Fatal("after revoke the daemon must own again")
	}
}

func TestRevokeEscalatesOnDeadline(t *testing.T) {
	l := NewLease()
	if _, err := l.grantTo(1); err != nil {
		t.Fatal(err)
	}
	// No borrower acks; revoke must still return promptly via ctx deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { l.revokeLease(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("revokeLease did not return on ctx deadline (would-be unbounded wait)")
	}
	if !l.Owner() {
		t.Fatal("daemon must own after escalated revoke")
	}
}

// fakeEndpoint records park/grant ordering and the live-reader count so the test
// can assert the single-reader invariant under -race.
type fakeEndpoint struct {
	readers atomic.Int32
	maxSeen atomic.Int32
	grants  atomic.Int32
}

func (f *fakeEndpoint) Drain(ctx context.Context) error {
	n := f.readers.Add(1)
	for {
		if cur := f.maxSeen.Load(); n > cur {
			f.maxSeen.CompareAndSwap(cur, n)
		}
		select {
		case <-ctx.Done():
			f.readers.Add(-1)
			return ctx.Err()
		case <-time.After(time.Millisecond):
		}
	}
}
func (f *fakeEndpoint) Grant() (HandoffRef, error) { f.grants.Add(1); return fakeRef{}, nil }
func (f *fakeEndpoint) Revoke(context.Context) error { return nil }
func (f *fakeEndpoint) Resize(Winsize) error         { return nil }
func (f *fakeEndpoint) Close() error                 { return nil }

type fakeRef struct{}

func (fakeRef) Transport() Transport { return TransportSCMFD }

type fakeSender struct{}

func (fakeSender) Send(net.Conn, HandoffRef) error { return nil }

func TestSessionParkBeforeGrant(t *testing.T) {
	fe := &fakeEndpoint{}
	s := NewSession(fe, fakeSender{})
	s.Start()

	for i := 0; i < 200; i++ {
		revoke, err := s.Attach(nil)
		if err != nil {
			t.Fatalf("attach %d: %v", i, err)
		}
		if s.Owner() {
			t.Fatalf("attach %d: must not be daemon-owned while granted", i)
		}
		// borrower yields, daemon steals back
		go func() { <-revoke; s.AckYield() }()
		s.Steal(context.Background())
		if !s.Owner() {
			t.Fatalf("attach %d: daemon must own after steal", i)
		}
	}
	if got := fe.maxSeen.Load(); got > 1 {
		t.Fatalf("single-reader invariant violated: saw %d concurrent drainers", got)
	}
	if got := fe.grants.Load(); got != 200 {
		t.Fatalf("expected 200 grants, got %d", got)
	}
	_ = s.Close()
}
