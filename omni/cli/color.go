package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/Shaik-Sirajuddin/memory/cli/theme"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// applyRole wraps text with the active theme colour for the given role.
func applyRole(r theme.Role, text string) string {
	return theme.Active().Apply(r, text)
}

// renderHelp prints colourised help for cmd to w using the active theme.
func renderHelp(cmd *cobra.Command, w io.Writer) {
	t := theme.Active()

	fmt.Fprintf(w, "%s  %s\n\n", t.Apply(theme.RoleSectionHead, "Usage:"), cmd.UseLine())

	if cmd.Long != "" {
		fmt.Fprintf(w, "%s\n\n", cmd.Long)
	} else if cmd.Short != "" {
		fmt.Fprintf(w, "%s\n\n", cmd.Short)
	}

	subs := cmd.Commands()
	visible := subs[:0]
	for _, s := range subs {
		if !s.Hidden {
			visible = append(visible, s)
		}
	}
	if len(visible) > 0 {
		fmt.Fprintf(w, "%s\n", t.Apply(theme.RoleSectionHead, "Available Commands:"))
		for _, s := range visible {
			fmt.Fprintf(w, "  %-20s %s\n", t.Apply(theme.RoleCommand, s.Name()), s.Short)
		}
		fmt.Fprintln(w)
	}

	flagLines := collectFlagLines(t, cmd.Flags())
	if len(flagLines) > 0 {
		fmt.Fprintf(w, "%s\n", t.Apply(theme.RoleSectionHead, "Flags:"))
		fmt.Fprintln(w, strings.Join(flagLines, "\n"))
	}

	inheritedLines := collectFlagLines(t, cmd.InheritedFlags())
	if len(inheritedLines) > 0 {
		fmt.Fprintf(w, "%s\n", t.Apply(theme.RoleSectionHead, "Global Flags:"))
		fmt.Fprintln(w, strings.Join(inheritedLines, "\n"))
	}
}

func collectFlagLines(t *theme.Theme, fs *pflag.FlagSet) []string {
	var lines []string
	fs.VisitAll(func(f *pflag.Flag) {
		shorthand := "    "
		if f.Shorthand != "" {
			shorthand = "-" + f.Shorthand + ", "
		}
		name := shorthand + "--" + f.Name
		valType := ""
		if f.Value.Type() != "bool" {
			valType = " " + t.Apply(theme.RoleFlagValue, "<"+f.Value.Type()+">")
		}
		required := ""
		if f.Annotations != nil {
			if _, ok := f.Annotations[cobra.BashCompOneRequiredFlag]; ok {
				required = " " + t.Apply(theme.RoleRequired, "(required)")
			}
		}
		lines = append(lines, fmt.Sprintf("  %s%s%s\n      %s",
			t.Apply(theme.RoleFlagName, name), valType, required,
			t.Apply(theme.RoleFlagDesc, f.Usage),
		))
	})
	return lines
}
