package zellij

import (
	"fmt"
	"strings"

	"github.com/Shaik-Sirajuddin/memory/terminal"
)

type ZellijTransformer struct{}

// ToNative converts a canonical Layout to a zellij KDL layout file.
// All panes are rendered with start_suspended=true.
// The tab at FocusedTab index gets focus=true (caller passes index via context; here we accept it as a param to keep Transformer pure).
func (t *ZellijTransformer) ToNative(layout terminal.Layout) ([]byte, error) {
	return t.ToNativeWithFocus(layout, 0)
}

func (t *ZellijTransformer) ToNativeWithFocus(layout terminal.Layout, focusedTab int) ([]byte, error) {
	var b strings.Builder
	b.WriteString("layout {\n")

	for i, tab := range layout.Tabs {
		focusAttr := ""
		if i == focusedTab {
			focusAttr = " focus=true"
		}
		if tab.Name != "" {
			fmt.Fprintf(&b, "    tab name=%q%s {\n", tab.Name, focusAttr)
		} else {
			fmt.Fprintf(&b, "    tab%s {\n", focusAttr)
		}

		// Tab-level command rendered as a pane when no explicit panes defined
		if len(tab.Panes) == 0 && tab.Command != "" {
			b.WriteString(renderPane(tab.Command, layout.Dir, true, 2))
		}

		for _, pane := range tab.Panes {
			dir := pane.Dir
			if dir == "" {
				dir = layout.Dir
			}
			b.WriteString(renderPane(pane.Command, dir, pane.StartSuspended, 2))
		}

		b.WriteString("    }\n")
	}

	b.WriteString("}\n")
	return []byte(b.String()), nil
}

// FromNative parses the KDL bytes emitted by ToNativeWithFocus back into a Layout.
// It handles the exact KDL structure this transformer produces; it is not a general KDL parser.
func (t *ZellijTransformer) FromNative(data []byte) (terminal.Layout, error) {
	lines := strings.Split(string(data), "\n")
	var layout terminal.Layout

	const (
		scopeLayout = "layout"
		scopeTab    = "tab"
		scopePane   = "pane"
	)

	var scopeStack []string
	tabIdx := -1
	paneIdx := -1

	for _, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}

		switch {
		case line == "layout {":
			scopeStack = append(scopeStack, scopeLayout)

		case strings.HasPrefix(line, "tab"):
			tab := terminal.TabLayout{Name: extractQuoted(line, "name=")}
			layout.Tabs = append(layout.Tabs, tab)
			tabIdx = len(layout.Tabs) - 1
			paneIdx = -1
			if strings.HasSuffix(line, "{") {
				scopeStack = append(scopeStack, scopeTab)
			}

		case strings.HasPrefix(line, "pane") && tabIdx >= 0:
			pane := terminal.PaneLayout{
				Command:        extractQuoted(line, "command="),
				StartSuspended: strings.Contains(line, "start_suspended=true"),
			}
			layout.Tabs[tabIdx].Panes = append(layout.Tabs[tabIdx].Panes, pane)
			paneIdx = len(layout.Tabs[tabIdx].Panes) - 1
			if strings.HasSuffix(line, "{") {
				scopeStack = append(scopeStack, scopePane)
			}

		case strings.HasPrefix(line, "cwd ") && paneIdx >= 0 && tabIdx >= 0:
			layout.Tabs[tabIdx].Panes[paneIdx].Dir = extractQuoted(line, "cwd ")

		case line == "}":
			if len(scopeStack) > 0 {
				popped := scopeStack[len(scopeStack)-1]
				scopeStack = scopeStack[:len(scopeStack)-1]
				switch popped {
				case scopePane:
					paneIdx = -1
				case scopeTab:
					tabIdx = -1
					paneIdx = -1
				}
			}
		}
	}

	return layout, nil
}

// extractQuoted extracts the double-quoted value immediately following prefix in line.
// E.g. extractQuoted(`tab name="main" focus=true`, "name=") → "main".
func extractQuoted(line, prefix string) string {
	idx := strings.Index(line, prefix+`"`)
	if idx == -1 {
		return ""
	}
	start := idx + len(prefix) + 1
	end := strings.Index(line[start:], `"`)
	if end == -1 {
		return ""
	}
	return line[start : start+end]
}

func renderPane(command, dir string, suspended bool, indent int) string {
	pad := strings.Repeat("    ", indent)
	var b strings.Builder
	if command != "" {
		fmt.Fprintf(&b, "%spane command=%q start_suspended=%v {\n", pad, command, suspended)
		if dir != "" {
			fmt.Fprintf(&b, "%s    cwd %q\n", pad, dir)
		}
		fmt.Fprintf(&b, "%s}\n", pad)
	} else {
		b.WriteString(pad + "pane\n")
	}
	return b.String()
}
