package handoff

import "fmt"

// pidverify.go holds the OS-agnostic PID-verification decision for the Windows
// relay pipe (H4). SDDL owner-only ACL on the pipe is necessary but NOT sufficient:
// any local process can win the CreateFile race on the pipe NAME. So after
// ConnectNamedPipe the daemon calls GetNamedPipeClientProcessId and verifies the
// connecting PID == the PID it expected (handed out-of-band over the control conn),
// else DisconnectNamedPipe. Keeping the decision pure makes it unit-testable on
// Linux without a real pipe.

// ErrPIDMismatch is returned by verifyClientPID when the connecting process is not
// the expected client. The caller MUST DisconnectNamedPipe on this error.
type ErrPIDMismatch struct {
	Want, Got uint32
}

func (e *ErrPIDMismatch) Error() string {
	return fmt.Sprintf("handoff: relay pipe PID verify failed: want %d, got %d", e.Want, e.Got)
}

// verifyClientPID returns nil iff got == want (and want != 0). A zero expected PID
// is treated as a misconfiguration and rejected — we never accept an unverifiable
// connection on the relay pipe.
func verifyClientPID(want, got uint32) error {
	if want == 0 {
		return &ErrPIDMismatch{Want: want, Got: got}
	}
	if got != want {
		return &ErrPIDMismatch{Want: want, Got: got}
	}
	return nil
}
