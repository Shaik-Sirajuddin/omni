//go:build linux || darwin

package handoff

import "os"

// NewUnixEndpoint builds an Endpoint that lends the given PTY master fd via
// SCM_RIGHTS. Unix/darwin only.
func NewUnixEndpoint(master *os.File) Endpoint { return newUnixEndpoint(master) }

// NewSender returns the platform HandoffSender (SCM_RIGHTS on Unix/darwin).
func NewSender() HandoffSender { return unixSender{} }
