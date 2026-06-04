//go:build windows

package handoff

import (
	"context"
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// conpty_windows.go is the REAL Win32 ConPTY + relay-pipe mechanism behind the
// conpty seam. Everything platform-specific lives here; the testable pump and
// endpoint core (relaypump.go / winendpoint_core.go) never import this.
//
// Design (plan §7/§19.0, Option B relay): the daemon CREATES a ConPTY, spawns the
// child attached to it, and is the SOLE, ALWAYS-ON reader of the ConPTY output
// pipe (B1). Bytes are relayed to a per-attach named pipe the client opens by name.

// kernel32 ConPTY procs not exported by x/sys/windows are declared lazily.
var (
	modkernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procCreatePseudoConsole = modkernel32.NewProc("CreatePseudoConsole")
	procResizePseudoConsole = modkernel32.NewProc("ResizePseudoConsole")
	procClosePseudoConsole  = modkernel32.NewProc("ClosePseudoConsole")
)

// hpcon is the opaque HPCON pseudoconsole handle.
type hpcon windows.Handle

// winConPTY is the real conpty seam implementation.
type winConPTY struct {
	mu sync.Mutex

	hpc hpcon

	// The four pipe ends of the ConPTY. The ConPTY owns inRead/outWrite; the daemon
	// keeps inWrite (to feed the child) and outRead (to read child output).
	inRead   windows.Handle
	inWrite  windows.Handle
	outRead  windows.Handle
	outWrite windows.Handle

	proc   windows.Handle // child process handle
	thread windows.Handle
	job    windows.Handle // kill-on-close Job Object (H5 kill-tree)

	out      *overlappedReader // always-on output reader seam (B1)
	attrList *windows.ProcThreadAttributeListContainer

	pendingName string // relay pipe name currently being served (published pre-connect)

	closed bool
}

// NewWinConPTY creates a ConPTY of the given size and spawns commandLine attached to
// it. commandLine is a full Windows command line (e.g. `cmd.exe /c echo MARKER`).
// The returned seam is wired into newWinEndpoint.
func NewWinConPTY(size Winsize, commandLine string) (*winConPTY, error) {
	w := &winConPTY{}

	// Child stdin stays an anonymous pipe (we write it synchronously via rawWrite):
	// inRead is read by the ConPTY, inWrite is written by us.
	if err := windows.CreatePipe(&w.inRead, &w.inWrite, nil, 0); err != nil {
		return nil, fmt.Errorf("CreatePipe(in): %w", err)
	}
	// Child stdout MUST be an overlapped-capable pipe: the always-on reader
	// (overlappedReader) does overlapped ReadFile + CancelIoEx (B1/M3). Anonymous
	// CreatePipe pipes are SYNCHRONOUS-ONLY — an overlapped ReadFile on one silently
	// blocks synchronously forever and CancelIoEx cannot cancel it (the cause of the
	// first Windows CI hang). Use a named-pipe pair whose READ end is FILE_FLAG_OVERLAPPED.
	outRead, outWrite, oerr := newOverlappedOutputPipe()
	if oerr != nil {
		w.cleanupPartial()
		return nil, oerr
	}
	w.outRead, w.outWrite = outRead, outWrite

	// CreatePseudoConsole(size COORD, hInput, hOutput, dwFlags, &hpcon).
	coord := packCoord(size)
	r1, _, _ := procCreatePseudoConsole.Call(
		uintptr(coord),
		uintptr(w.inRead),
		uintptr(w.outWrite),
		0,
		uintptr(unsafe.Pointer(&w.hpc)),
	)
	if r1 != 0 { // S_OK == 0
		w.cleanupPartial()
		return nil, fmt.Errorf("CreatePseudoConsole: HRESULT 0x%x", r1)
	}

	// The ConPTY now owns inRead and outWrite; close OUR copies so EOF propagates.
	windows.CloseHandle(w.inRead)
	w.inRead = 0
	windows.CloseHandle(w.outWrite)
	w.outWrite = 0

	if err := w.spawn(commandLine); err != nil {
		w.cleanupPartial()
		return nil, err
	}

	w.out = newOverlappedReader(w.outRead, w.proc)
	return w, nil
}

// spawn launches the child via STARTUPINFOEX + PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE
// and assigns it to a kill-on-close Job Object so Close() kills the whole tree.
func (w *winConPTY) spawn(commandLine string) error {
	attrList, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return fmt.Errorf("NewProcThreadAttributeList: %w", err)
	}
	w.attrList = attrList
	// UpdateProcThreadAttribute copies `size` bytes from `value`; the attribute value
	// for PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE is the HPCON handle itself, so point at
	// the handle variable with size == sizeof(HPCON).
	if err := attrList.Update(
		windows.PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE,
		unsafe.Pointer(&w.hpc),
		unsafe.Sizeof(w.hpc),
	); err != nil {
		attrList.Delete()
		w.attrList = nil
		return fmt.Errorf("UpdateProcThreadAttribute(PSEUDOCONSOLE): %w", err)
	}

	// Job Object: kill the child tree when the job handle closes (SIGHUP-equiv).
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("CreateJobObject: %w", err)
	}
	w.job = job
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
	); err != nil {
		return fmt.Errorf("SetInformationJobObject(kill-on-close): %w", err)
	}

	cmd16, err := windows.UTF16PtrFromString(commandLine)
	if err != nil {
		return fmt.Errorf("UTF16PtrFromString: %w", err)
	}
	si := new(windows.StartupInfoEx)
	si.Cb = uint32(unsafe.Sizeof(*si))
	si.ProcThreadAttributeList = attrList.List()

	pi := new(windows.ProcessInformation)
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_UNICODE_ENVIRONMENT)
	// CreateProcess with a writable copy of the command line (Win32 may mutate it).
	if err := windows.CreateProcess(
		nil,
		cmd16,
		nil, nil,
		false, // do not inherit handles — the ConPTY plumbs them
		flags,
		nil, nil,
		&si.StartupInfo,
		pi,
	); err != nil {
		return fmt.Errorf("CreateProcess: %w", err)
	}
	w.proc = pi.Process
	w.thread = pi.Thread

	// Assign to the job AFTER creation. (Acceptable for our single-child case; a
	// fully race-free variant would create suspended + assign + resume.)
	if err := windows.AssignProcessToJobObject(w.job, w.proc); err != nil {
		return fmt.Errorf("AssignProcessToJobObject: %w", err)
	}
	return nil
}

func (w *winConPTY) output() outputSource { return w.out }

// pendingPipeName returns the relay pipe name newSink is currently serving (set
// before ConnectNamedPipe so a connecting client can learn it). Empty otherwise.
func (w *winConPTY) pendingPipeName() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.pendingName
}

// newSink creates the per-attach relay named pipe (server side) with an owner-only
// SDDL ACL (H4), FILE_FLAG_OVERLAPPED and an explicit out buffer; waits for the
// client to ConnectNamedPipe; PID-verifies it (H4); and returns the connected sink.
func (w *winConPTY) newSink(ctx context.Context, clientPID uint32) (relaySink, *pipeRef, error) {
	name := newPipeName()

	name16, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return nil, nil, err
	}

	// Owner-only ACL: D:P(A;;GA;;;OW) — grant GENERIC_ALL to the OWNER only, and the
	// protected (P) flag blocks inherited ACEs. SDDL is necessary but NOT sufficient
	// (H4) — the PID check below is the real gate.
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;OW)")
	if err != nil {
		return nil, nil, fmt.Errorf("SecurityDescriptorFromString: %w", err)
	}
	sa := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: sd,
	}

	// Publish the name BEFORE ConnectNamedPipe so the client (which learns it over
	// the control conn) can CreateFile while Grant is still blocked on connect.
	w.mu.Lock()
	w.pendingName = name
	w.mu.Unlock()
	defer func() {
		w.mu.Lock()
		w.pendingName = ""
		w.mu.Unlock()
	}()

	const outBufSize = 64 * 1024
	pipe, err := windows.CreateNamedPipe(
		name16,
		windows.PIPE_ACCESS_OUTBOUND|windows.FILE_FLAG_OVERLAPPED|windows.FILE_FLAG_FIRST_PIPE_INSTANCE,
		windows.PIPE_TYPE_BYTE|windows.PIPE_REJECT_REMOTE_CLIENTS,
		1, // nMaxInstances = 1 (single client)
		outBufSize,
		0,
		0,
		sa,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("CreateNamedPipe: %w", err)
	}

	if err := w.waitConnect(ctx, pipe); err != nil {
		windows.CloseHandle(pipe)
		return nil, nil, err
	}

	// H4: PID-verify the connecting process; disconnect on mismatch.
	var gotPID uint32
	if err := windows.GetNamedPipeClientProcessId(pipe, &gotPID); err != nil {
		windows.DisconnectNamedPipe(pipe)
		windows.CloseHandle(pipe)
		return nil, nil, fmt.Errorf("GetNamedPipeClientProcessId: %w", err)
	}
	if verr := verifyClientPID(clientPID, gotPID); verr != nil {
		windows.DisconnectNamedPipe(pipe)
		windows.CloseHandle(pipe)
		return nil, nil, verr
	}

	return newPipeSink(pipe), &pipeRef{Name: name}, nil
}

// waitConnect performs an overlapped ConnectNamedPipe and waits for either the
// client or ctx cancellation.
func (w *winConPTY) waitConnect(ctx context.Context, pipe windows.Handle) error {
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(ev)
	ov := &windows.Overlapped{HEvent: ev}

	cerr := windows.ConnectNamedPipe(pipe, ov)
	if cerr == windows.ERROR_PIPE_CONNECTED {
		return nil // client connected between create and connect; fine
	}
	if cerr != nil && cerr != windows.ERROR_IO_PENDING {
		return fmt.Errorf("ConnectNamedPipe: %w", cerr)
	}

	// Wait for the connect to complete or ctx to cancel.
	done := make(chan error, 1)
	go func() {
		_, werr := windows.WaitForSingleObject(ev, windows.INFINITE)
		done <- werr
	}()
	select {
	case <-ctx.Done():
		windows.CancelIoEx(pipe, ov)
		<-done
		return ctx.Err()
	case werr := <-done:
		if werr != nil {
			return werr
		}
		var transferred uint32
		return windows.GetOverlappedResult(pipe, ov, &transferred, true)
	}
}

// resize maps to ResizePseudoConsole (M1).
func (w *winConPTY) resize(size Winsize) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return fmt.Errorf("handoff: conpty closed")
	}
	coord := packCoord(size)
	r1, _, _ := procResizePseudoConsole.Call(uintptr(w.hpc), uintptr(coord))
	if r1 != 0 {
		return fmt.Errorf("ResizePseudoConsole: HRESULT 0x%x", r1)
	}
	return nil
}

// writeInput feeds raw VT input to the child, intercepting Ctrl-C (0x03) →
// GenerateConsoleCtrlEvent(CTRL_C_EVENT) (plan §18.1). Callers in the input relay
// path use this instead of writing the inWrite handle directly.
func (w *winConPTY) writeInput(p []byte) (int, error) {
	written := 0
	for i := 0; i < len(p); i++ {
		if p[i] == 0x03 { // Ctrl-C
			// Flush bytes before the Ctrl-C, then raise the console ctrl event.
			if i > written {
				if err := w.rawWrite(p[written:i]); err != nil {
					return written, err
				}
			}
			// processGroupID 0 == every process attached to this console.
			if err := windows.GenerateConsoleCtrlEvent(windows.CTRL_C_EVENT, 0); err != nil {
				return i, err
			}
			written = i + 1
		}
	}
	if written < len(p) {
		if err := w.rawWrite(p[written:]); err != nil {
			return written, err
		}
	}
	return len(p), nil
}

func (w *winConPTY) rawWrite(p []byte) error {
	var done uint32
	return windows.WriteFile(w.inWrite, p, &done, nil)
}

// close tears down the ConPTY and kills the child tree (closing the job handle
// triggers JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE). Idempotent.
func (w *winConPTY) close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	w.mu.Unlock()

	if w.out != nil {
		_ = w.out.Close()
	}
	// Close the pseudoconsole first (drops the ConPTY's ends of the pipes).
	if w.hpc != 0 {
		procClosePseudoConsole.Call(uintptr(w.hpc))
		w.hpc = 0
	}
	// Closing the job kills the child tree (SIGHUP-equiv).
	if w.job != 0 {
		windows.CloseHandle(w.job)
		w.job = 0
	}
	if w.thread != 0 {
		windows.CloseHandle(w.thread)
		w.thread = 0
	}
	if w.proc != 0 {
		windows.CloseHandle(w.proc)
		w.proc = 0
	}
	if w.inWrite != 0 {
		windows.CloseHandle(w.inWrite)
		w.inWrite = 0
	}
	if w.outRead != 0 {
		windows.CloseHandle(w.outRead)
		w.outRead = 0
	}
	if w.attrList != nil {
		w.attrList.Delete()
		w.attrList = nil
	}
	return nil
}

// cleanupPartial closes whatever handles were opened during a failed constructor.
func (w *winConPTY) cleanupPartial() {
	for _, h := range []*windows.Handle{&w.inRead, &w.inWrite, &w.outRead, &w.outWrite, &w.proc, &w.thread, &w.job} {
		if *h != 0 {
			windows.CloseHandle(*h)
			*h = 0
		}
	}
	if w.hpc != 0 {
		procClosePseudoConsole.Call(uintptr(w.hpc))
		w.hpc = 0
	}
	if w.attrList != nil {
		w.attrList.Delete()
		w.attrList = nil
	}
}

// newOverlappedOutputPipe creates a byte pipe whose READ end supports overlapped
// I/O. Anonymous CreatePipe pipes do NOT support overlapped I/O — a ConPTY-killer:
// an overlapped ReadFile on one blocks synchronously and CancelIoEx cannot cancel
// it. So the read end is a named-pipe SERVER opened FILE_FLAG_OVERLAPPED, and the
// write end (handed to CreatePseudoConsole as hOutput) is a synchronous client
// handle. Both ends connect within this process, so no ConnectNamedPipe is needed:
// the CreateFile below establishes the connection immediately.
func newOverlappedOutputPipe() (read, write windows.Handle, err error) {
	name, err := windows.UTF16PtrFromString(newPipeName())
	if err != nil {
		return 0, 0, err
	}
	const bufSize = 64 * 1024
	read, err = windows.CreateNamedPipe(
		name,
		windows.PIPE_ACCESS_INBOUND|windows.FILE_FLAG_OVERLAPPED|windows.FILE_FLAG_FIRST_PIPE_INSTANCE,
		windows.PIPE_TYPE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS,
		1, bufSize, bufSize, 0, nil,
	)
	if err != nil {
		return 0, 0, fmt.Errorf("CreateNamedPipe(out): %w", err)
	}
	write, err = windows.CreateFile(
		name,
		windows.GENERIC_WRITE,
		0, nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		windows.CloseHandle(read)
		return 0, 0, fmt.Errorf("CreateFile(out write): %w", err)
	}
	return read, write, nil
}

// packCoord packs a Winsize into the DWORD that CreatePseudoConsole/Resize expect:
// a COORD is {X int16 (cols), Y int16 (rows)} laid out little-endian in a uintptr.
func packCoord(s Winsize) uint32 {
	return uint32(s.Cols) | uint32(s.Rows)<<16
}

var _ conpty = (*winConPTY)(nil)
