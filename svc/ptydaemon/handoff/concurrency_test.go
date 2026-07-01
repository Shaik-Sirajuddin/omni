package handoff

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// ---- shared fakes for the concurrency-core tests ----

// countingEndpoint records every Drain/Grant/Revoke so tests can assert the
// single-reader invariant (Draining XOR Granted) and call counts under -race.
// Drain blocks on ctx; while running it holds a "live reader" token and records
// the maximum number of concurrently-live drainers ever observed.
type countingEndpoint struct {
	liveDrainers atomic.Int32
	maxDrainers  atomic.Int32
	drainStarts  atomic.Int32
	grants       atomic.Int32
	revokes      atomic.Int32
	resizes      atomic.Int32
	closes       atomic.Int32

	lastWinsize atomic.Pointer[Winsize]
	grantErr    atomic.Pointer[error]

	// grantActive is true between a successful Grant and its Revoke — i.e. a client
	// logically holds the read side. tornReader latches true if a Drain ever starts
	// while a grant is active: that is two concurrent readers (the BLOCKER the
	// grantTo-error-path startDrainLocked() used to cause). maxDrainers alone cannot
	// see this, because a granted client is not a "drainer".
	grantActive atomic.Bool
	tornReader  atomic.Bool
}

func (e *countingEndpoint) Drain(ctx context.Context) error {
	e.drainStarts.Add(1)
	if e.grantActive.Load() {
		e.tornReader.Store(true) // a second reader started while a client holds the grant
	}
	n := e.liveDrainers.Add(1)
	for {
		if cur := e.maxDrainers.Load(); n > cur {
			e.maxDrainers.CompareAndSwap(cur, n)
		}
		select {
		case <-ctx.Done():
			e.liveDrainers.Add(-1)
			return ctx.Err()
		case <-time.After(200 * time.Microsecond):
		}
	}
}

func (e *countingEndpoint) Grant() (HandoffRef, error) {
	// Invariant: no drainer may be live at the instant of Grant (park-before-grant).
	if live := e.liveDrainers.Load(); live != 0 {
		// record the violation as a sentinel so the test fails loudly
		var err error = errTornGrant
		e.grantErr.Store(&err)
	}
	e.grants.Add(1)
	if p := e.grantErr.Load(); p != nil {
		return nil, *p
	}
	e.grantActive.Store(true)
	return fakeRef{}, nil
}

func (e *countingEndpoint) Revoke(context.Context) error {
	e.revokes.Add(1)
	e.grantActive.Store(false)
	return nil
}
func (e *countingEndpoint) Resize(s Winsize) error {
	e.resizes.Add(1)
	w := s
	e.lastWinsize.Store(&w)
	return nil
}
func (e *countingEndpoint) Close() error { e.closes.Add(1); return nil }

var errTornGrant = &tornGrantError{}

type tornGrantError struct{}

func (*tornGrantError) Error() string { return "handoff: Grant observed a live drainer (torn read)" }

// ---- Invariant 1: single-reader (Draining XOR Granted) ----

func TestSingleReaderNeverTwoDrainers(t *testing.T) {
	ce := &countingEndpoint{}
	s := NewSession(ce, fakeSender{})
	s.Start()
	defer s.Close()

	// Hammer attach/steal concurrently across many goroutines. Only one Attach
	// can win the lease at a time (the others get ErrLeaseGranted); the winner
	// then yields and we steal it back. Throughout, the endpoint asserts that no
	// Grant ever coincided with a live drainer and that maxDrainers never > 1.
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 60; i++ {
				revoke, err := s.Attach(nil)
				if err == ErrLeaseGranted {
					continue // someone else holds it; legal
				}
				if err != nil {
					t.Errorf("attach: %v", err)
					return
				}
				done := make(chan struct{})
				go func() { <-revoke; s.AckYield(); close(done) }()
				s.Steal(context.Background())
				<-done
			}
		}()
	}
	wg.Wait()

	if ce.tornReader.Load() {
		t.Fatal("single-reader invariant violated: a drainer ran while a grant was active")
	}
	if got := ce.maxDrainers.Load(); got > 1 {
		t.Fatalf("single-reader invariant violated: %d concurrent drainers", got)
	}
	if p := ce.grantErr.Load(); p != nil {
		t.Fatalf("park-before-grant violated: %v", *p)
	}
	if !s.Owner() {
		t.Fatal("daemon must own after all clients yielded")
	}
}

// ---- Invariant 3: park-before-grant happens-before (drainer provably stopped) ----

// orderEndpoint records the precise interleaving of drain-start, drain-stop and
// grant events to prove the drainer's last read completes-before the grant.
type orderEndpoint struct {
	mu       sync.Mutex
	events   []string
	draining bool
}

func (e *orderEndpoint) log(s string) { e.mu.Lock(); e.events = append(e.events, s); e.mu.Unlock() }

func (e *orderEndpoint) Drain(ctx context.Context) error {
	e.mu.Lock()
	e.draining = true
	e.mu.Unlock()
	e.log("drain-start")
	<-ctx.Done()
	e.mu.Lock()
	e.draining = false
	e.mu.Unlock()
	e.log("drain-stop")
	return ctx.Err()
}

func (e *orderEndpoint) Grant() (HandoffRef, error) {
	e.mu.Lock()
	d := e.draining
	e.mu.Unlock()
	if d {
		e.log("GRANT-WHILE-DRAINING")
		return nil, errTornGrant
	}
	e.log("grant")
	return fakeRef{}, nil
}
func (e *orderEndpoint) Revoke(context.Context) error { return nil }
func (e *orderEndpoint) Resize(Winsize) error         { return nil }
func (e *orderEndpoint) Close() error                 { return nil }

func TestParkHappensBeforeGrant(t *testing.T) {
	oe := &orderEndpoint{}
	s := NewSession(oe, fakeSender{})
	s.Start()
	defer s.Close()

	for i := 0; i < 50; i++ {
		revoke, err := s.Attach(nil)
		if err != nil {
			t.Fatalf("attach %d: %v", i, err)
		}
		go func() { <-revoke; s.AckYield() }()
		s.Steal(context.Background())
	}

	oe.mu.Lock()
	ev := append([]string(nil), oe.events...)
	oe.mu.Unlock()

	// Every "grant" must be immediately preceded by a "drain-stop" with no
	// intervening "drain-start" — i.e. the drainer parked before the grant.
	lastDrain := ""
	for _, e := range ev {
		switch e {
		case "GRANT-WHILE-DRAINING":
			t.Fatalf("torn read: grant observed while draining; events=%v", ev)
		case "drain-start", "drain-stop":
			lastDrain = e
		case "grant":
			if lastDrain != "drain-stop" {
				t.Fatalf("grant not preceded by drain-stop (was %q); events=%v", lastDrain, ev)
			}
		}
	}
}

// ---- Invariant 2: asymmetric authority — double-grant rejected, daemon reclaims ----

func TestDoubleGrantRejectedAndDaemonReclaims(t *testing.T) {
	s := NewSession(&countingEndpoint{}, fakeSender{})
	s.Start()
	defer s.Close()

	revoke, err := s.Attach(nil)
	if err != nil {
		t.Fatalf("first attach: %v", err)
	}
	if s.Owner() {
		t.Fatal("must not be daemon-owned while granted")
	}
	// Second attach while granted must fail with ErrLeaseGranted and must NOT
	// disturb the existing grant.
	if _, err := s.Attach(nil); err != ErrLeaseGranted {
		t.Fatalf("double attach: want ErrLeaseGranted, got %v", err)
	}
	if s.Owner() {
		t.Fatal("failed double-attach must not have reclaimed the lease")
	}
	// The borrower has no verb to keep the lease: daemon steals it back.
	go func() { <-revoke; s.AckYield() }()
	s.Steal(context.Background())
	if !s.Owner() {
		t.Fatal("daemon must own after steal-back")
	}
}

// ---- Invariant 4: revoke never blocks unbounded (escalation on deadline) ----

func TestSessionStealEscalatesOnDeadline(t *testing.T) {
	s := NewSession(&countingEndpoint{}, fakeSender{})
	s.Start()
	defer s.Close()

	if _, err := s.Attach(nil); err != nil {
		t.Fatalf("attach: %v", err)
	}
	// Borrower NEVER acks. Steal must still return promptly via the ctx deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	start := time.Now()
	go func() { s.Steal(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Steal did not return on ctx deadline (unbounded wait)")
	}
	if el := time.Since(start); el > time.Second {
		t.Fatalf("Steal took %v; expected ~deadline (escalation)", el)
	}
	if !s.Owner() {
		t.Fatal("daemon must own after escalated steal")
	}
}

func TestRevokeIdempotentNoBorrower(t *testing.T) {
	l := NewLease()
	// Revoking while already draining is a no-op and must not block.
	done := make(chan struct{})
	go func() { l.revokeLease(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("revokeLease on a draining lease blocked")
	}
	if !l.Owner() {
		t.Fatal("lease should remain daemon-owned")
	}
}

// ---- Detach idempotency ----

func TestDetachIdempotent(t *testing.T) {
	ce := &countingEndpoint{}
	s := NewSession(ce, fakeSender{})
	s.Start()
	defer s.Close()

	if _, err := s.Attach(nil); err != nil {
		t.Fatalf("attach: %v", err)
	}
	// First Detach reclaims; subsequent Detach calls must be safe no-ops.
	s.Detach()
	if !s.Owner() {
		t.Fatal("daemon must own after detach")
	}
	for i := 0; i < 5; i++ {
		s.Detach() // must not panic, deadlock, or double-start drainers
	}
	if !s.Owner() {
		t.Fatal("daemon must remain owner after repeated detach")
	}
	if got := ce.maxDrainers.Load(); got > 1 {
		t.Fatalf("repeated detach spawned %d concurrent drainers", got)
	}
}

// ---- Steal WITHOUT an ack (relies purely on escalation) and WITH an ack ----

func TestStealWithAndWithoutAck(t *testing.T) {
	t.Run("with-ack", func(t *testing.T) {
		s := NewSession(&countingEndpoint{}, fakeSender{})
		s.Start()
		defer s.Close()
		revoke, err := s.Attach(nil)
		if err != nil {
			t.Fatal(err)
		}
		acked := make(chan struct{})
		go func() { <-revoke; s.AckYield(); close(acked) }()
		s.Steal(context.Background())
		select {
		case <-acked:
		case <-time.After(time.Second):
			t.Fatal("borrower never observed revoke / acked")
		}
		if !s.Owner() {
			t.Fatal("daemon must own")
		}
	})
	t.Run("without-ack", func(t *testing.T) {
		s := NewSession(&countingEndpoint{}, fakeSender{})
		s.Start()
		defer s.Close()
		if _, err := s.Attach(nil); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		s.Steal(ctx) // no ack; escalation path
		if !s.Owner() {
			t.Fatal("daemon must own after no-ack steal")
		}
	})
}

// ---- Grant-while-granted at the Session level returns and restarts drainer ----

func TestGrantWhileGrantedRejectedNoSecondReader(t *testing.T) {
	ce := &countingEndpoint{}
	s := NewSession(ce, fakeSender{})
	s.Start()

	revoke, err := s.Attach(nil)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	before := ce.drainStarts.Load()

	// Second attach while the first client holds the lease must be REJECTED and
	// must NOT spawn a drainer — doing so would create a second reader racing the
	// holder's dup'd fd (the BLOCKER). The holder keeps the read side; the caller
	// must Steal first if it wants to take over.
	if _, err := s.Attach(nil); err != ErrLeaseGranted {
		t.Fatalf("want ErrLeaseGranted, got %v", err)
	}
	// Give any (erroneous) restarted drainer a chance to run, then assert none did.
	time.Sleep(20 * time.Millisecond)
	if got := ce.drainStarts.Load(); got != before {
		t.Fatalf("failed second attach spawned a drainer (drainStarts %d -> %d) — second reader racing the holder", before, got)
	}
	if ce.tornReader.Load() {
		t.Fatal("a drainer started while a grant was active — two concurrent readers")
	}
	if s.Owner() {
		t.Fatal("holder still has the lease; daemon must not own")
	}

	// Steal restores draining cleanly.
	go func() { <-revoke; s.AckYield() }()
	s.Steal(context.Background())
	if !s.Owner() {
		t.Fatal("daemon must own after steal")
	}
	if ce.tornReader.Load() {
		t.Fatal("torn reader after steal")
	}
	_ = s.Close()
}

// ---- Resize forwards to the endpoint regardless of lease state ----

func TestResizeForwardsAlways(t *testing.T) {
	ce := &countingEndpoint{}
	s := NewSession(ce, fakeSender{})
	s.Start()
	defer s.Close()

	want := Winsize{Cols: 132, Rows: 43}
	if err := s.Resize(want); err != nil {
		t.Fatalf("resize while draining: %v", err)
	}
	if _, err := s.Attach(nil); err != nil {
		t.Fatalf("attach: %v", err)
	}
	want2 := Winsize{Cols: 80, Rows: 24}
	if err := s.Resize(want2); err != nil {
		t.Fatalf("resize while granted: %v", err)
	}
	if got := ce.lastWinsize.Load(); got == nil || *got != want2 {
		t.Fatalf("resize not forwarded: got %v want %v", got, want2)
	}
	if got := ce.resizes.Load(); got != 2 {
		t.Fatalf("expected 2 resizes, got %d", got)
	}
}
