package theme

const (
	reset = "\033[0m"
	bold  = "\033[1m"
	dim   = "\033[2m"

	fgBlack   = "\033[30m"
	fgRed     = "\033[31m"
	fgGreen   = "\033[32m"
	fgYellow  = "\033[33m"
	fgBlue    = "\033[34m"
	fgMagenta = "\033[35m"
	fgCyan    = "\033[36m"
	fgWhite   = "\033[37m"

	fgBrightRed     = "\033[91m"
	fgBrightGreen   = "\033[92m"
	fgBrightYellow  = "\033[93m"
	fgBrightBlue    = "\033[94m"
	fgBrightMagenta = "\033[95m"
	fgBrightCyan    = "\033[96m"
	fgBrightWhite   = "\033[97m"

	fgOrange = "\033[38;5;208m"
	fgNavy   = "\033[38;5;25m"
	fgTeal   = "\033[38;5;37m"
	fgAmber  = "\033[38;5;214m"
	fgPurple = "\033[38;5;135m"
	fgDimGray = "\033[38;5;243m"
)

// Role identifies a semantic colour slot in the terminal UI.
type Role int

const (
	RoleCommand     Role = iota
	RoleFlagName         // --flag, -f
	RoleFlagValue        // <value> placeholder
	RoleFlagDesc         // flag description text
	RoleRequired         // (required) marker
	RoleSectionHead      // USAGE / FLAGS / COMMANDS headers
	RolePositional       // positional arg name

	RoleLogDebug
	RoleLogInfo
	RoleLogWarn
	RoleLogError
	RoleLogFatal

	roleSentinel // must stay last
)

// Theme maps semantic roles to ANSI escape sequences.
// Uses a fixed-size array so Color() is a single index with no heap alloc.
type Theme struct {
	Name   string
	colors [roleSentinel]string
}

// Color returns the ANSI prefix for r, or "" when the theme uses no colour for that role.
func (t *Theme) Color(r Role) string {
	if r < 0 || r >= roleSentinel {
		return ""
	}
	return t.colors[r]
}

// Reset is the ANSI reset sequence.
const Reset = reset

// Apply wraps text with the theme colour for role r and appends a reset.
// When the colour string is empty, text is returned unchanged.
func (t *Theme) Apply(r Role, text string) string {
	c := t.Color(r)
	if c == "" {
		return text
	}
	return c + text + reset
}

// Dark builds the default dark-background theme.
func Dark() *Theme {
	t := &Theme{Name: "dark"}
	t.colors[RoleCommand] = bold
	t.colors[RoleFlagName] = fgBrightCyan
	t.colors[RoleFlagValue] = fgYellow
	t.colors[RoleFlagDesc] = fgWhite
	t.colors[RoleRequired] = fgBrightRed
	t.colors[RoleSectionHead] = bold + fgBrightWhite
	t.colors[RolePositional] = fgCyan
	t.colors[RoleLogDebug] = dim + fgDimGray
	t.colors[RoleLogInfo] = fgBrightGreen
	t.colors[RoleLogWarn] = fgBrightYellow
	t.colors[RoleLogError] = fgBrightRed
	t.colors[RoleLogFatal] = bold + fgBrightRed
	return t
}

// DarkDim builds a low-contrast dark-background theme.
func DarkDim() *Theme {
	t := &Theme{Name: "dark-dim"}
	t.colors[RoleCommand] = bold
	t.colors[RoleFlagName] = fgCyan
	t.colors[RoleFlagValue] = dim + fgYellow
	t.colors[RoleFlagDesc] = dim + fgWhite
	t.colors[RoleRequired] = fgRed
	t.colors[RoleSectionHead] = bold
	t.colors[RolePositional] = dim + fgCyan
	t.colors[RoleLogDebug] = dim + fgDimGray
	t.colors[RoleLogInfo] = fgGreen
	t.colors[RoleLogWarn] = fgYellow
	t.colors[RoleLogError] = fgRed
	t.colors[RoleLogFatal] = bold + fgRed
	return t
}

// Light builds a light-background theme.
func Light() *Theme {
	t := &Theme{Name: "light"}
	t.colors[RoleCommand] = bold
	t.colors[RoleFlagName] = fgNavy
	t.colors[RoleFlagValue] = fgAmber
	t.colors[RoleFlagDesc] = fgBlack
	t.colors[RoleRequired] = fgRed
	t.colors[RoleSectionHead] = bold + fgBlack
	t.colors[RolePositional] = fgTeal
	t.colors[RoleLogDebug] = dim + fgDimGray
	t.colors[RoleLogInfo] = fgGreen
	t.colors[RoleLogWarn] = fgAmber
	t.colors[RoleLogError] = fgRed
	t.colors[RoleLogFatal] = bold + fgRed
	return t
}

// Colorblind builds a theme safe for deuteranopia/protanopia.
// Uses blue/orange/purple instead of red/green distinctions.
func Colorblind() *Theme {
	t := &Theme{Name: "colorblind"}
	t.colors[RoleCommand] = bold
	t.colors[RoleFlagName] = fgBrightBlue
	t.colors[RoleFlagValue] = fgOrange
	t.colors[RoleFlagDesc] = fgWhite
	t.colors[RoleRequired] = fgOrange
	t.colors[RoleSectionHead] = bold + fgBrightWhite
	t.colors[RolePositional] = fgBlue
	t.colors[RoleLogDebug] = dim + fgDimGray
	t.colors[RoleLogInfo] = fgBrightBlue
	t.colors[RoleLogWarn] = fgOrange
	t.colors[RoleLogError] = fgPurple
	t.colors[RoleLogFatal] = bold + fgPurple
	return t
}
