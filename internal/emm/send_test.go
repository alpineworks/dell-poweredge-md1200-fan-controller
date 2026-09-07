package emm

import (
	"errors"
	"syscall"
	"testing"
	"time"

	"go.bug.st/serial"
)

// fakePort is a serial.Port whose Write and Drain behaviour is scripted.
type fakePort struct {
	writes     [][]byte
	writeErrs  []error // consumed one per Write call; nil = success
	drainErrs  []error // consumed one per Drain call; nil = success
	drainCalls int
}

func (f *fakePort) Write(p []byte) (int, error) {
	var err error
	if len(f.writeErrs) > 0 {
		err, f.writeErrs = f.writeErrs[0], f.writeErrs[1:]
	}
	if err != nil {
		return 0, err
	}
	f.writes = append(f.writes, append([]byte(nil), p...))
	return len(p), nil
}

func (f *fakePort) Drain() error {
	f.drainCalls++
	if len(f.drainErrs) > 0 {
		var err error
		err, f.drainErrs = f.drainErrs[0], f.drainErrs[1:]
		return err
	}
	return nil
}

func (f *fakePort) SetMode(*serial.Mode) error                           { return nil }
func (f *fakePort) Read([]byte) (int, error)                             { return 0, nil }
func (f *fakePort) ResetInputBuffer() error                              { return nil }
func (f *fakePort) ResetOutputBuffer() error                             { return nil }
func (f *fakePort) SetDTR(bool) error                                    { return nil }
func (f *fakePort) SetRTS(bool) error                                    { return nil }
func (f *fakePort) SetReadTimeout(time.Duration) error                   { return nil }
func (f *fakePort) GetModemStatusBits() (*serial.ModemStatusBits, error) { return nil, nil }
func (f *fakePort) Close() error                                         { return nil }
func (f *fakePort) Break(time.Duration) error                            { return nil }

func newTestClient(p *fakePort) *Client {
	return &Client{port: p, retry: RetryPolicy{
		InitialInterval: time.Millisecond,
		MaxInterval:     2 * time.Millisecond,
		MaxElapsedTime:  50 * time.Millisecond,
	}}
}

func TestSendRetriesDrainEINTRImmediately(t *testing.T) {
	p := &fakePort{drainErrs: []error{syscall.EINTR, syscall.EINTR, nil}}
	c := newTestClient(p)

	if err := c.SetFanSpeed(CommandShutup, 20); err != nil {
		t.Fatalf("SetFanSpeed: %v", err)
	}
	if p.drainCalls != 3 {
		t.Errorf("drain calls = %d, want 3", p.drainCalls)
	}
	if len(p.writes) != 1 || string(p.writes[0]) != "\r_shutup 20\r" {
		t.Errorf("writes = %q, want one write of \"\\r_shutup 20\\r\"", p.writes)
	}
}

func TestSendRetriesWriteEINTR(t *testing.T) {
	p := &fakePort{writeErrs: []error{syscall.EINTR, nil}}
	c := newTestClient(p)

	if err := c.Send("_temp_rd"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(p.writes) != 1 {
		t.Errorf("writes = %d, want 1 successful write", len(p.writes))
	}
}

func TestSendBacksOffOnTransientError(t *testing.T) {
	p := &fakePort{writeErrs: []error{syscall.EAGAIN, syscall.EAGAIN, nil}}
	c := newTestClient(p)

	if err := c.Send("_temp_rd"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if len(p.writes) != 1 {
		t.Errorf("writes = %d, want 1 successful write after backoff", len(p.writes))
	}
}

func TestSendGivesUpImmediatelyOnPermanentError(t *testing.T) {
	// EBADF is what a write to a closed descriptor returns; serial.PortError
	// codes are classified the same way but cannot be constructed from
	// outside the library.
	p := &fakePort{writeErrs: []error{syscall.EBADF, syscall.EBADF, syscall.EBADF}}
	c := newTestClient(p)

	start := time.Now()
	err := c.Send("_temp_rd")
	if err == nil {
		t.Fatal("Send: expected error for closed descriptor")
	}
	if !errors.Is(err, syscall.EBADF) {
		t.Errorf("error %v does not wrap EBADF", err)
	}
	if len(p.writeErrs) != 2 {
		t.Errorf("remaining scripted errors = %d, want 2 (no retries after permanent error)", len(p.writeErrs))
	}
	if time.Since(start) > 20*time.Millisecond {
		t.Errorf("Send took %s, permanent errors must not wait for backoff", time.Since(start))
	}
}

func TestSendFailsAfterBudgetExhausted(t *testing.T) {
	errs := make([]error, 100)
	for i := range errs {
		errs[i] = syscall.EAGAIN
	}
	p := &fakePort{writeErrs: errs}
	c := newTestClient(p)

	if err := c.Send("_temp_rd"); err == nil {
		t.Fatal("Send: expected error once retry budget is exhausted")
	}
	if len(p.writes) != 0 {
		t.Errorf("writes = %d, want 0", len(p.writes))
	}
}
