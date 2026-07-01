//go:build linux || darwin

package handoff

import (
	"errors"
	"net"

	"golang.org/x/sys/unix"
)

// unixSender transmits a dup'd master fd to the client over a *net.UnixConn via
// SCM_RIGHTS ancillary data. The fd is closed after the send (the client now
// holds its own copy).
type unixSender struct{}

func (unixSender) Send(conn net.Conn, ref HandoffRef) error {
	uc, ok := conn.(*net.UnixConn)
	if !ok {
		return errors.New("handoff: unix sender requires *net.UnixConn")
	}
	sr, ok := ref.(*scmRef)
	if !ok {
		return errors.New("handoff: unix sender requires an scm-fd ref")
	}
	defer unix.Close(sr.fd)
	rights := unix.UnixRights(sr.fd)
	_, _, err := uc.WriteMsgUnix([]byte("ok\n"), rights, nil)
	return err
}
