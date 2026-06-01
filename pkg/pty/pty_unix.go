//go:build linux || darwin

package pty

import (
	"os"
	"os/exec"

	cpty "github.com/creack/pty"
)

type unixPTY struct {
	master *os.File
	slave  *os.File // nil after StartCmd (owned by child process)
}

// Open creates a new PTY pair sized cols×rows without starting a command.
func Open(cols, rows uint16) (PTY, error) {
	m, s, err := cpty.Open()
	if err != nil {
		return nil, err
	}
	if err := cpty.Setsize(m, &cpty.Winsize{Cols: cols, Rows: rows}); err != nil {
		m.Close()
		s.Close()
		return nil, err
	}
	return &unixPTY{master: m, slave: s}, nil
}

// StartCmd starts cmd attached to a new PTY and returns a PTY whose Master()
// is the controlling master fd. Slave() returns nil; the child process owns it.
func StartCmd(cmd *exec.Cmd) (PTY, error) {
	master, err := cpty.Start(cmd)
	if err != nil {
		return nil, err
	}
	return &unixPTY{master: master}, nil
}

func (p *unixPTY) Master() *os.File { return p.master }
func (p *unixPTY) Slave() *os.File  { return p.slave }

func (p *unixPTY) Resize(cols, rows uint16) error {
	return cpty.Setsize(p.master, &cpty.Winsize{Cols: cols, Rows: rows})
}

func (p *unixPTY) Close() error {
	if p.slave != nil {
		p.slave.Close()
	}
	return p.master.Close()
}

// GetWinsize returns the terminal window size of f.
func GetWinsize(f *os.File) (Winsize, error) {
	ws, err := cpty.GetsizeFull(f)
	if err != nil {
		return Winsize{}, err
	}
	return Winsize{Rows: ws.Rows, Cols: ws.Cols, X: ws.X, Y: ws.Y}, nil
}

// SetWinsize sets the terminal window size of f.
func SetWinsize(f *os.File, ws Winsize) error {
	return cpty.Setsize(f, &cpty.Winsize{Rows: ws.Rows, Cols: ws.Cols, X: ws.X, Y: ws.Y})
}
