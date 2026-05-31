package theme

import (
	"os"
	"sync/atomic"

	"golang.org/x/term"
)

var active atomic.Pointer[Theme]
var noColor atomic.Bool

func init() {
	active.Store(Dark())
}

// Activate sets the active theme by name and detects whether colour output is
// possible. When stdout is not a TTY (piped, redirected, CI), colours are
// suppressed automatically. Unknown names fall back to "dark".
func Activate(name string) {
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		noColor.Store(true)
	}
	active.Store(build(name))
}

// NoColor reports whether colour output has been suppressed.
func NoColor() bool { return noColor.Load() }

// Active returns the currently active theme.
func Active() *Theme {
	return active.Load()
}

// Names returns the list of available built-in theme names.
func Names() []string {
	return []string{"dark", "dark-dim", "light", "colorblind"}
}

func build(name string) *Theme {
	switch name {
	case "dark-dim":
		return DarkDim()
	case "light":
		return Light()
	case "colorblind":
		return Colorblind()
	default:
		return Dark()
	}
}
