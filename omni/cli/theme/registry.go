package theme

import "sync/atomic"

var active atomic.Pointer[Theme]

func init() {
	active.Store(Dark())
}

// Activate sets the active theme by name.
// Unknown names fall back to "dark". Only the chosen theme is allocated.
func Activate(name string) {
	active.Store(build(name))
}

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
