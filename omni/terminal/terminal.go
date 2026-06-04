package terminal

type Layout struct {
	Dir  string
	Tabs []TabLayout
}

type TabLayout struct {
	Name    string
	Command string
	Panes   []PaneLayout
}

type PaneLayout struct {
	Command        string
	Dir            string
	StartSuspended bool
}

type Template struct {
	Name   string
	Layout Layout
}

type Session struct {
	Name string
}

type SessionParams struct {
	Name       string
	Layout     Layout
	FocusedTab int
}

type Transformer interface {
	ToNative(Layout) ([]byte, error)
	FromNative([]byte) (Layout, error)
}

type Terminal interface {
	CheckInstallation() error
	Install() error
	Templates() []Template
	InitializeSession(SessionParams) error
	ListSessions() ([]Session, error)
	CheckSessionExists(name string) (bool, error)
	GetSession(name string) (*Session, error)
	Transformer() Transformer
}
