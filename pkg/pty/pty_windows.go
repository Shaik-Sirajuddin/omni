//go:build windows

package pty

import (
	"errors"
	"os"
	"os/exec"
)

var errNotSupported = errors.New("pty: not supported on this platform")

// Open is not supported on Windows.
func Open(cols, rows uint16) (PTY, error) { return nil, errNotSupported }

// StartCmd is not supported on Windows.
func StartCmd(cmd *exec.Cmd) (PTY, error) { return nil, errNotSupported }

// GetWinsize is not supported on Windows.
func GetWinsize(f *os.File) (Winsize, error) { return Winsize{}, errNotSupported }

// SetWinsize is not supported on Windows.
func SetWinsize(f *os.File, ws Winsize) error { return errNotSupported }
