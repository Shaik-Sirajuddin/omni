package cli

import (
	"fmt"
	"os"
	"strings"

	omnemory "github.com/Shaik-Sirajuddin/memory/memory"
	"github.com/spf13/cobra"
)

// newMemoryCommand returns the "memory" command group.
func (c *DefaultCli) newMemoryCommand() *cobra.Command {
	memCmd := &cobra.Command{
		Use:   "memory",
		Short: "Validate and lint memory layout trees",
	}
	memCmd.AddCommand(c.newMemoryValidateCommand())
	memCmd.AddCommand(c.newMemoryLintCommand())
	return memCmd
}

// newMemoryValidateCommand returns "omni memory validate".
//
//	omni memory validate [<path>]
//	omni memory validate --agent <name>
//	omni memory validate --version v3
func (c *DefaultCli) newMemoryValidateCommand() *cobra.Command {
	var agentName string
	var versionStr string
	var fix bool
	var memRoot string

	cmd := &cobra.Command{
		Use:   "validate [<path>]",
		Short: "Validate a memory tree or agent dir against its versioned contract",
		Long: `Validate resolves the version of the target (from metadata.yaml or .memory.lock),
loads the matching memory/v<x>/semantics.yaml contract, and checks structural
and format rules. Exits non-zero when error-level violations are found.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			targetPath := "."
			if len(args) == 1 {
				targetPath = args[0]
			}

			// Parse --version flag (accept "v3", "3", etc.)
			forceVersion := 0
			if versionStr != "" {
				v := strings.TrimPrefix(strings.TrimSpace(versionStr), "v")
				n := 0
				if _, err := fmt.Sscanf(v, "%d", &n); err != nil || n < 1 {
					return fmt.Errorf("invalid --version %q (expected e.g. v3 or 3)", versionStr)
				}
				forceVersion = n
			}

			opts := omnemory.Options{
				ForceVersion: forceVersion,
				AgentName:    agentName,
				MemoryRoot:   memRoot,
				Fix:          fix,
			}

			report, err := omnemory.Validate(targetPath, opts)
			if err != nil {
				return err
			}

			printMemoryReport(cmd, report)

			if report.HasViolations() {
				return fmt.Errorf("memory validate: %d error(s) found", countErrors(report))
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&agentName, "agent", "", "Validate a single named agent dir under the memory root")
	cmd.Flags().StringVar(&versionStr, "version", "", "Force contract version (e.g. v3); overrides auto-detect")
	cmd.Flags().BoolVar(&fix, "fix", false, "Scaffold missing memory.md files where safe (v3 only)")
	cmd.Flags().StringVar(&memRoot, "memory-root", "", "Override the memory root dir (where v<x>/ contract dirs live)")

	return cmd
}

// newMemoryLintCommand returns "omni memory lint".
//
//	omni memory lint [<path>]
func (c *DefaultCli) newMemoryLintCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "lint [<path>]",
		Short: "Format-only check of every *.yaml and memory.md in the memory tree",
		Long: `Lint parses every *.yaml file under the target path and reports YAML syntax
errors. Unlike validate, lint does not require a versioned contract; it is a
quick sanity check on file format only.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			targetPath := "."
			if len(args) == 1 {
				targetPath = args[0]
			}
			report, err := omnemory.Lint(targetPath)
			if err != nil {
				return err
			}
			printMemoryReport(cmd, report)
			if report.HasViolations() {
				return fmt.Errorf("memory lint: %d error(s) found", countErrors(report))
			}
			return nil
		},
	}
	return cmd
}

// ─── Output helpers ──────────────────────────────────────────────────────────

func printMemoryReport(cmd *cobra.Command, report omnemory.Report) {
	out := cmd.OutOrStdout()
	if len(report.Violations) == 0 {
		fmt.Fprintln(out, "OK")
		return
	}
	for _, v := range report.Violations {
		label := strings.ToUpper(string(v.Severity))
		rel := v.Path
		if cwd, err := os.Getwd(); err == nil {
			if r, err := relativePath(cwd, v.Path); err == nil {
				rel = r
			}
		}
		fmt.Fprintf(out, "[%s] %s: %s\n", label, rel, v.Message)
	}
	errors := countErrors(report)
	warnings := countWarnings(report)
	if errors > 0 || warnings > 0 {
		fmt.Fprintf(out, "\n%d error(s), %d warning(s)\n", errors, warnings)
	}
}

func countErrors(r omnemory.Report) int {
	n := 0
	for _, v := range r.Violations {
		if v.Severity == omnemory.SeverityError {
			n++
		}
	}
	return n
}

func countWarnings(r omnemory.Report) int {
	n := 0
	for _, v := range r.Violations {
		if v.Severity == omnemory.SeverityWarning {
			n++
		}
	}
	return n
}

// relativePath returns path relative to base, falling back to path unchanged.
func relativePath(base, path string) (string, error) {
	if !strings.HasPrefix(path, base) {
		return path, nil
	}
	rel := strings.TrimPrefix(path, base)
	rel = strings.TrimPrefix(rel, string(os.PathSeparator))
	if rel == "" {
		return ".", nil
	}
	return rel, nil
}
