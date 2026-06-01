// Package pty provides a cross-platform PTY interface used by svc/ptydaemon.
// Platform implementations are selected at compile time via build tags.
package pty

import "os"

// Winsize holds terminal dimensions in characters and pixels.
type Winsize struct {
	Rows, Cols uint16
	X, Y       uint16 // pixel dimensions
}

// PTY represents an open pseudo-terminal pair.
type PTY interface {
	// Master returns the PTY master file; the daemon reads child output here.
	Master() *os.File
	// Slave returns the PTY slave file, or nil when a child process owns it
	// (i.e. when the PTY was created via StartCmd).
	Slave() *os.File
	// Resize sets the terminal window size.
	Resize(cols, rows uint16) error
	// Close closes the master (and slave if open).
	Close() error
}
