package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"

	"log"

	"github.com/Shaik-Sirajuddin/memory/cli/theme"
	"github.com/Shaik-Sirajuddin/memory/config"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent"
	"github.com/Shaik-Sirajuddin/memory/mcp/mcp/runner"
	"github.com/Shaik-Sirajuddin/memory/omniagent"
	"github.com/Shaik-Sirajuddin/memory/operator"
	"github.com/Shaik-Sirajuddin/memory/operator/impl/defaults"
	omnisandbox "github.com/Shaik-Sirajuddin/memory/sandbox"
	sandbox "github.com/Shaik-Sirajuddin/memory/sandbox/provider"
	"github.com/Shaik-Sirajuddin/memory/store/codesession"
	"github.com/knadh/koanf/providers/posflag"
	"github.com/knadh/koanf/v2"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// DefaultCli wires cobra namespaces for config and agent operations.
type DefaultCli struct {
	root           *cobra.Command
	operator       operator.Operator
	configResolver config.OmniConfigResolver
	version        string
}

// Entrypoint builds the CLI root command.
func Entrypoint(op operator.Operator, resolver config.OmniConfigResolver) *DefaultCli {
	return EntrypointWithVersion(op, resolver, "dev")
}

// EntrypointWithVersion builds the CLI root command with release metadata.
func EntrypointWithVersion(op operator.Operator, resolver config.OmniConfigResolver, version string) *DefaultCli {
	if version == "" {
		version = "dev"
	}
	c := &DefaultCli{
		operator:       op,
		configResolver: resolver,
		version:        version,
	}

	root := &cobra.Command{
		Use:     "omni",
		Short:   "Omni agent CLI",
		Version: version,
	}
	root.SetVersionTemplate("{{.Version}}\n")
	root.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		renderHelp(cmd, os.Stdout)
	})

	root.AddCommand(c.newVersionCommand())
	root.AddCommand(c.newHookCommand())
	root.AddCommand(c.newConfigCommand())
	root.AddCommand(c.newAgentCommand())
	root.AddCommand(c.newTeamCommand())
	root.AddCommand(c.newTeamInitCommand())
	root.AddCommand(c.newDoctorCommand())
	root.AddCommand(c.newServerCommand())
	root.AddCommand(c.newAxoLinkCommand())
	root.AddCommand(c.newMemoryCommand())

	c.root = root
	return c
}

func (c *DefaultCli) newVersionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print omni version",
		RunE: func(cmd *cobra.Command, args []string) error {
			version := c.version
			if version == "" {
				version = "dev"
			}
			fmt.Fprintln(cmd.OutOrStdout(), version)
			return nil
		},
	}
}

// Install executes the CLI.
func (c *DefaultCli) Install() error {
	if c == nil || c.root == nil {
		return errors.New("cli is not initialized")
	}
	c.activateTheme()
	normalizeSessionIDFlag(os.Args[1:])
	return c.root.Execute()
}

// activateTheme resolves the active colour theme from env → config → default.
func (c *DefaultCli) activateTheme() {
	name := os.Getenv("OMNI_THEME")
	if name == "" && c.configResolver != nil {
		if cfg, err := ResolveConfig(c.configResolver); err == nil && cfg != nil {
			name = cfg.Theme
		}
	}
	theme.Activate(name)
	log.SetOutput(newLogWriter(os.Stderr))
}

func normalizeSessionIDFlag(args []string) {
	for i, arg := range args {
		if arg == "-sid" {
			args[i] = "--session-id"
		}
	}
}

func (c *DefaultCli) newConfigCommand() *cobra.Command {
	configCmd := &cobra.Command{
		Use:   "config",
		Short: "Manage omni config",
	}

	flags := config.ProvisionConfigGetFlags()
	getCmd := &cobra.Command{
		Use:   "get",
		Short: "Print resolved omni config",
		RunE: func(cmd *cobra.Command, args []string) error {
			if c.configResolver == nil {
				return errors.New("config resolver is required")
			}
			resolved := flags
			if err := loadFlags(cmd, &resolved); err != nil {
				return err
			}
			cfg, err := ResolveConfig(c.configResolver)
			if err != nil {
				return err
			}
			return printOutput("config.get", resolved.Output, cfg)
		},
	}
	getCmd.Flags().StringP("output", "o", flags.Output, "Output format: yaml|table|json")
	configCmd.AddCommand(getCmd)

	configCmd.AddCommand(c.newConfigSetCommand())

	return configCmd
}

func (c *DefaultCli) newConfigSetCommand() *cobra.Command {
	flags := config.ProvisionConfigSetFlags()

	setCmd := &cobra.Command{
		Use:   "set",
		Short: "Update omni config feature flags",
		RunE: func(cmd *cobra.Command, args []string) error {
			if c.configResolver == nil {
				return errors.New("config resolver is required")
			}
			resolved := flags
			if err := loadFlags(cmd, &resolved); err != nil {
				return err
			}

			cfg, err := ResolveConfig(c.configResolver)
			if err != nil {
				return err
			}

			if resolved.Memory != "" {
				v, parseErr := strconv.ParseBool(resolved.Memory)
				if parseErr != nil {
					return fmt.Errorf("invalid --memory value %q: %w", resolved.Memory, parseErr)
				}
				cfg.Features.Memory = v
			}
			if resolved.AutoSync != "" {
				v, parseErr := strconv.ParseBool(resolved.AutoSync)
				if parseErr != nil {
					return fmt.Errorf("invalid --autosync value %q: %w", resolved.AutoSync, parseErr)
				}
				cfg.Features.AutoSync = v
			}

			if resolved.Theme != "" {
				valid := theme.Names()
				ok := false
				for _, n := range valid {
					if n == resolved.Theme {
						ok = true
						break
					}
				}
				if !ok {
					return fmt.Errorf("invalid --theme value %q (valid: %s)", resolved.Theme, strings.Join(valid, ", "))
				}
				cfg.Theme = resolved.Theme
			}

			if err := SaveConfig(c.configResolver, cfg); err != nil {
				return err
			}

			return printJSON(cfg)
		},
	}

	setCmd.Flags().String("memory", flags.Memory, "Set memory feature (true|false); empty leaves value unchanged")
	setCmd.Flags().String("autosync", flags.AutoSync, "Set autosync feature (true|false); empty leaves value unchanged")
	setCmd.Flags().String("theme", flags.Theme, "Set terminal colour theme (dark|dark-dim|light|colorblind); empty leaves value unchanged")

	return setCmd
}

func (c *DefaultCli) newAgentCommand() *cobra.Command {
	agentCmd := &cobra.Command{
		Use:   "agent",
		Short: "Manage agents through operator",
	}

	agentCmd.AddCommand(c.newAgentDiscoverCommand())

	agentCmd.AddCommand(c.newAgentListCommand())
	agentCmd.AddCommand(c.newAgentCreateCommand())
	agentCmd.AddCommand(c.newAgentResumeCommand())
	agentCmd.AddCommand(c.newAgentUpdateCommand())
	agentCmd.AddCommand(c.newAgentUpgradeCommand())
	agentCmd.AddCommand(c.newAgentDeleteCommand())
	agentCmd.AddCommand(c.newAgentSwitchProviderCommand())
	agentCmd.AddCommand(c.newAgentSandboxCommand())
	agentCmd.AddCommand(c.newAgentPipeCommand())
	agentCmd.AddCommand(c.newAgentExecCommand())
	agentCmd.AddCommand(c.newAgentStopCommand())
	agentCmd.AddCommand(c.newAgentDetachCommand())
	agentCmd.AddCommand(c.newAgentGetCommand())

	return agentCmd
}

// newAgentGetCommand returns the "agent <name> get <key>" subcommand group.
// Supported keys:
//
//	sys-instructs  — print the resolved filesystem path to the agent's
//	                 system-instructions for its current layout version.
func (c *DefaultCli) newAgentGetCommand() *cobra.Command {
	var workspace string

	getCmd := &cobra.Command{
		Use:   "get <name> <key>",
		Short: "Get a named property for an agent",
		Long: `Get a named property for a named agent.  The agent's layout version is
auto-detected from its on-disk metadata.yaml.

Supported keys:
  sys-instructs  Print the resolved path to the agent's system instructions.
                 Version mapping:
                   v1 → <agentdir>/entry/instructions   (directory)
                   v2 → <agentdir>/instructions/memory.md
                   v3 → <agentdir>/instructions/memory.md`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			key := args[1]

			ws := workspace
			if ws == "" {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("resolve workspace: %w", err)
				}
				ws = wd
			}

			switch key {
			case "sys-instructs":
				path, err := operator.ResolveAgentSysInstructionsPath(ws, name)
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), path)
				return nil
			default:
				return fmt.Errorf("unknown key %q (supported: sys-instructs)", key)
			}
		},
	}

	getCmd.Flags().StringVar(&workspace, "workspace", "", "Workspace directory (default: current directory)")
	return getCmd
}

func (c *DefaultCli) newTeamInitCommand() *cobra.Command {
	flags := config.ProvisionTeamInitFlags()
	var teamName string
	var remote string

	cmd := &cobra.Command{
		Use:   "team-init",
		Short: "Initialize a team for the current workspace",
		RunE: func(cmd *cobra.Command, args []string) error {
			if c.operator == nil {
				return errors.New("operator is required")
			}

			resolved := flags
			if err := loadFlags(cmd, &resolved); err != nil {
				return err
			}
			wd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("resolve current workspace: %w", err)
			}
			memoryDir := fmt.Sprintf("%s/%s", wd, "memory")
			_, existedErr := os.Stat(memoryDir)
			existed := existedErr == nil

			if err := c.operator.TeamInit(operator.TeamInitParams{
				Workspace: sandbox.WorkspaceDir(wd),
				RepoURL:   resolved.RepoURL,
				Name:      teamName,
				Remote:    remote,
			}); err != nil {
				return err
			}
			if existed {
				fmt.Println("team reinitialized")
			} else {
				fmt.Println("team initialized")
			}
			return nil
		},
	}

	cmd.Flags().String("repo_url", flags.RepoURL, "Optional git repository URL used to add memory as submodule")
	cmd.Flags().StringVar(&teamName, "name", "", "Team name (defaults to workspace folder basename)")
	cmd.Flags().StringVar(&remote, "remote", "localhost", "Remote address this workspace belongs to")
	return cmd
}

func (c *DefaultCli) newTeamCommand() *cobra.Command {
	teamCmd := &cobra.Command{
		Use:   "team",
		Short: "Manage teams (workspace groups)",
	}

	teamCmd.AddCommand(c.newTeamListCommand())
	teamCmd.AddCommand(c.newTeamGetCommand())
	teamCmd.AddCommand(c.newTeamInitSubcommand())

	return teamCmd
}

func (c *DefaultCli) newDoctorCommand() *cobra.Command {
	doctorCmd := &cobra.Command{
		Use:   "doctor",
		Short: "Validate and install sandbox runtime prerequisites",
	}

	doctorCmd.AddCommand(c.newDoctorHooksCommand())
	// doctorCmd.AddCommand(c.newDoctorCheckCommand())
	// doctorCmd.AddCommand(c.newDoctorInstallCommand())
	return doctorCmd
}

func (c *DefaultCli) newDoctorCheckCommand() *cobra.Command {
	flags := config.ProvisionDoctorCheckFlags()

	cmd := &cobra.Command{
		Use:   "check",
		Short: "Check sandbox runtime availability for this OS",
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved := flags
			if err := loadFlags(cmd, &resolved); err != nil {
				return err
			}
			status := omnisandbox.NewDoctor().Health()
			return printOutput("doctor.check", resolved.Output, status)
		},
	}

	cmd.Flags().StringP("output", "o", flags.Output, "Output format: table|yaml|json")
	return cmd
}

func (c *DefaultCli) newDoctorInstallCommand() *cobra.Command {
	flags := config.ProvisionDoctorInstallFlags()

	cmd := &cobra.Command{
		Use:   "install",
		Short: "Install sandbox runtime dependencies when supported",
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved := flags
			if err := loadFlags(cmd, &resolved); err != nil {
				return err
			}
			doctor := omnisandbox.NewDoctor()
			if err := doctor.Install(); err != nil {
				return err
			}
			status := doctor.Health()
			return printOutput("doctor.install", resolved.Output, status)
		},
	}

	cmd.Flags().StringP("output", "o", flags.Output, "Output format: table|yaml|json")
	return cmd
}

func (c *DefaultCli) newServerCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Manage omni background services",
	}
	cmd.AddCommand(c.newServerStartCommand())
	cmd.AddCommand(c.newServerStopCommand())
	cmd.AddCommand(c.newServerStatusCommand())
	return cmd
}

func (c *DefaultCli) newServerStartCommand() *cobra.Command {
	var debug bool
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Start the PTY daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := runSystemctl("start"); err == nil {
				fmt.Println("server started")
				return nil
			}
			bin, err := resolvePTYDaemonBinary()
			if err != nil {
				return err
			}
			proc := exec.Command(bin)
			proc.Stdout = os.Stdout
			proc.Stderr = os.Stderr
			if debug {
				proc.Env = append(os.Environ(), "DEV=1")
			}
			if err := proc.Start(); err != nil {
				return fmt.Errorf("start ptydaemon: %w", err)
			}
			if err := os.WriteFile(ptyDaemonPIDFile(), []byte(strconv.Itoa(proc.Process.Pid)), 0o644); err != nil {
				return fmt.Errorf("write ptydaemon pid file: %w", err)
			}
			_ = proc.Process.Release()
			fmt.Println("server started")
			return nil
		},
	}
	cmd.Flags().BoolVarP(&debug, "debug", "d", false, "Start daemon in debug mode")
	return cmd
}

func (c *DefaultCli) newServerStopCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the PTY daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := runSystemctl("stop"); err == nil {
				_ = os.Remove(ptyDaemonPIDFile())
				fmt.Println("server stopped")
				return nil
			}
			pidData, err := os.ReadFile(ptyDaemonPIDFile())
			if err != nil {
				return fmt.Errorf("read ptydaemon pid file: %w", err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(pidData)))
			if err != nil {
				return fmt.Errorf("parse ptydaemon pid: %w", err)
			}
			proc, err := os.FindProcess(pid)
			if err != nil {
				return fmt.Errorf("find ptydaemon process: %w", err)
			}
			if err := proc.Signal(syscall.SIGTERM); err != nil {
				return fmt.Errorf("stop ptydaemon: %w", err)
			}
			_ = os.Remove(ptyDaemonPIDFile())
			fmt.Println("server stopped")
			return nil
		},
	}
}

type serverStatusResponse struct {
	PID      int                         `json:"pid"`
	Uptime   float64                     `json:"uptime"`
	Sessions int                         `json:"sessions"`
	Active   []serverStatusActiveSession `json:"active"`
}

type serverStatusActiveSession struct {
	AgentID   string `json:"agent_id"`
	SessionID string `json:"session_id"`
	PID       int    `json:"pid"`
	Status    string `json:"status"`
	StartedAt string `json:"started_at"`
}

func (c *DefaultCli) newServerStatusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Print PTY daemon status",
		RunE: func(cmd *cobra.Command, args []string) error {
			status, err := fetchServerStatus(ptyDaemonSocketPath())
			if err != nil {
				return printSystemctlStatus()
			}
			fmt.Printf("omni-server  pid=%d  uptime=%.0fs  sessions=%d\n", status.PID, status.Uptime, status.Sessions)
			if len(status.Active) == 0 {
				fmt.Println("(no active sessions)")
				return nil
			}
			for _, session := range status.Active {
				fmt.Printf("%s/%s  pid=%d  status=%s  started=%s\n",
					session.AgentID, session.SessionID, session.PID, session.Status, session.StartedAt)
			}
			return nil
		},
	}
}

func (c *DefaultCli) newTeamListCommand() *cobra.Command {
	flags := config.ProvisionTeamListFlags()

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List teams/workspaces",
		RunE: func(cmd *cobra.Command, args []string) error {
			if c.operator == nil {
				return errors.New("operator is required")
			}
			resolved := flags
			if err := loadFlags(cmd, &resolved); err != nil {
				return err
			}
			result, err := c.operator.ListWorkspaces(operator.ListWorkspacesParams{})
			if err != nil {
				return err
			}
			return printOutput("team.list", resolved.Output, result)
		},
	}

	cmd.Flags().StringP("output", "o", flags.Output, "Output format: table|yaml|json")
	return cmd
}

func (c *DefaultCli) newTeamInitSubcommand() *cobra.Command {
	flags := config.ProvisionTeamInitFlags()
	var teamName string
	var remote string

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize a team in the current workspace",
		RunE: func(cmd *cobra.Command, args []string) error {
			if c.operator == nil {
				return errors.New("operator is required")
			}

			resolved := flags
			if err := loadFlags(cmd, &resolved); err != nil {
				return err
			}
			wd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("resolve current workspace: %w", err)
			}
			memoryDir := fmt.Sprintf("%s/%s", wd, "memory")
			_, existedErr := os.Stat(memoryDir)
			existed := existedErr == nil

			if err := c.operator.TeamInit(operator.TeamInitParams{
				Workspace: sandbox.WorkspaceDir(wd),
				RepoURL:   resolved.RepoURL,
				Name:      teamName,
				Remote:    remote,
			}); err != nil {
				return err
			}
			if existed {
				fmt.Println("team reinitialized")
			} else {
				fmt.Println("team initialized")
			}
			return nil
		},
	}

	cmd.Flags().String("repo_url", flags.RepoURL, "Optional git repository URL used to add memory as submodule")
	cmd.Flags().StringVar(&teamName, "name", "", "Team name (defaults to workspace folder basename)")
	cmd.Flags().StringVar(&remote, "remote", "localhost", "Remote address this workspace belongs to")
	return cmd
}

func (c *DefaultCli) newTeamGetCommand() *cobra.Command {
	flags := config.ProvisionTeamGetFlags()

	getCmd := &cobra.Command{
		Use:   "get",
		Short: "Get a team/workspace by id",
		RunE: func(cmd *cobra.Command, args []string) error {
			resolved := flags
			if err := loadFlags(cmd, &resolved); err != nil {
				return err
			}
			if c.operator == nil {
				return errors.New("operator is required")
			}
			if resolved.ID == "" {
				wd, err := os.Getwd()
				if err != nil {
					return fmt.Errorf("resolve current workspace: %w", err)
				}
				workspaces, err := c.operator.ListWorkspaces(operator.ListWorkspacesParams{})
				if err != nil {
					return err
				}
				for _, team := range workspaces.Teams {
					if team != nil && team.WorkspaceDir == wd {
						resolved.ID = team.ID
						break
					}
				}
			}
			if resolved.ID == "" {
				return errors.New("workspace id is required and no team matched current working directory")
			}
			result, err := c.operator.GetWorkspace(operator.GetWorkSpaceParams{ID: resolved.ID})
			if err != nil {
				return err
			}
			return printOutput("team.get", resolved.Output, result)
		},
	}

	getCmd.Flags().String("id", flags.ID, "Workspace ID")
	getCmd.Flags().StringP("output", "o", flags.Output, "Output format: table|yaml|json")
	return getCmd
}

func (c *DefaultCli) newAgentDiscoverCommand() *cobra.Command {
	flags := config.ProvisionAgentDiscoverFlags()

	cmd := &cobra.Command{
		Use:   "discover",
		Short: "Discover available code agents",
		RunE: func(cmd *cobra.Command, args []string) error {
			if c.operator == nil {
				return errors.New("operator is required")
			}
			resolved := flags
			if err := loadFlags(cmd, &resolved); err != nil {
				return err
			}
			result, err := c.operator.DisoverCodeAgents()
			if err != nil {
				return err
			}
			return printOutput("agent.discover", resolved.Output, result)
		},
	}

	cmd.Flags().StringP("output", "o", flags.Output, "Output format: table|yaml|json")
	return cmd
}

func (c *DefaultCli) newAgentListCommand() *cobra.Command {
	flags := config.ProvisionAgentListFlags()

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List agents for a workspace",
		RunE: func(cmd *cobra.Command, args []string) error {
			if c.operator == nil {
				return errors.New("operator is required")
			}
			resolved := flags
			if err := loadFlags(cmd, &resolved); err != nil {
				return err
			}
			result, err := c.operator.ListCodeAgents(operator.GetCodeAgentsParams{
				Workspace: sandbox.WorkspaceDir(resolved.Workspace),
			})
			if err != nil {
				return err
			}
			return printOutput("agent.list", resolved.Output, result)
		},
	}

	listCmd.Flags().String("workspace", flags.Workspace, "Workspace directory")
	listCmd.Flags().StringP("output", "o", flags.Output, "Output format: table|yaml|json")

	return listCmd
}

func (c *DefaultCli) newAgentCreateCommand() *cobra.Command {
	flags := config.ProvisionAgentCreateFlags()
	var sessionID string

	createCmd := &cobra.Command{
		Use:   "init [name]",
		Short: "Initialize an agent in workspace",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if c.operator == nil {
				return errors.New("operator is required")
			}
			resolved := flags
			if err := loadFlags(cmd, &resolved); err != nil {
				return err
			}
			if len(args) == 1 {
				resolved.Name = args[0]
			}
			envMap, err := parseEnvSlice(resolved.ExtraEnvs)
			if err != nil {
				return err
			}
			var runCfg *omniagent.RunConfig
			if len(resolved.ExtraArgs) > 0 || len(envMap) > 0 {
				runCfg = &omniagent.RunConfig{ExtraArgs: resolved.ExtraArgs, Envs: envMap}
			}
			return c.operator.CreateAgent(operator.CreateAgentParams{
				Workspace:          sandbox.WorkspaceDir(resolved.Workspace),
				Name:               resolved.Name,
				Provider:           codeagent.Provider(resolved.Provider),
				Model:              resolved.Model,
				AllowGeneratedName: resolved.AllowGeneratedName,
				ResumeIfExists:     resolved.ResumeIfExists,
				Interactive:        resolved.Interactive,
				SessionID:          sessionID,
				RunConfig:          runCfg,
			})
		},
	}

	createCmd.Flags().String("workspace", flags.Workspace, "Workspace directory")
	createCmd.Flags().StringP("provider", "p", flags.Provider, "Agent provider (default: gemini)")
	createCmd.Flags().String("model", flags.Model, "Agent model (default depends on provider)")
	createCmd.Flags().Bool("allow_generated_name", flags.AllowGeneratedName, "Allow operator to generate agent name when name is empty")
	createCmd.Flags().BoolP("resume_if_exists", "r", flags.ResumeIfExists, "Resume agent when the provided name already exists in workspace")
	createCmd.Flags().Bool("interactive", flags.Interactive, "Launch agent after create")
	createCmd.Flags().StringVar(&sessionID, "session-id", "", "Optional session ID")
	createCmd.Flags().StringArray("arg", nil, "Pass-through flag appended to the runner command line (repeatable)")
	createCmd.Flags().StringArray("env", nil, "Extra env var injected into the runner process, KEY=VALUE (repeatable)")

	return createCmd
}

func (c *DefaultCli) newAgentResumeCommand() *cobra.Command {
	flags := config.ProvisionAgentResumeFlags()
	var sessionID string
	var detach bool

	cmd := &cobra.Command{
		Use:   "resume <name>",
		Short: "Resume an indexed agent by name",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if c.operator == nil {
				return errors.New("operator is required")
			}
			resolved := flags
			if err := loadFlags(cmd, &resolved); err != nil {
				return err
			}
			envMap, err := parseEnvSlice(resolved.ExtraEnvs)
			if err != nil {
				return err
			}
			var runCfg *omniagent.RunConfig
			if len(resolved.ExtraArgs) > 0 || len(envMap) > 0 {
				runCfg = &omniagent.RunConfig{ExtraArgs: resolved.ExtraArgs, Envs: envMap}
			}
			return c.operator.ResumeAgent(operator.ResumeAgentParams{
				Workspace:     sandbox.WorkspaceDir(resolved.Workspace),
				Name:          args[0],
				InitIfMissing: resolved.InitIfMissing,
				Provider:      codeagent.Provider(resolved.Provider),
				Model:         resolved.Model,
				SessionID:     sessionID,
				Detached:      detach,
				RunConfig:     runCfg,
				ClearArgs:     resolved.ClearArgs,
				ClearEnvs:     resolved.ClearEnvs,
			})
		},
	}

	cmd.Flags().String("workspace", flags.Workspace, "Workspace directory")
	cmd.Flags().BoolP("init_if_missing", "i", flags.InitIfMissing, "Create agent when requested name is not found in workspace")
	cmd.Flags().StringP("provider", "p", flags.Provider, "Provider used only when --init_if_missing creates a new agent")
	cmd.Flags().String("model", flags.Model, "Model used only when --init_if_missing creates a new agent")
	cmd.Flags().StringVar(&sessionID, "session-id", "", "Optional session ID")
	cmd.Flags().BoolVarP(&detach, "detach", "d", false, "Start PTY daemon session and return immediately without attaching")
	cmd.Flags().StringArray("arg", nil, "Pass-through flag appended to the runner command line (repeatable)")
	cmd.Flags().StringArray("env", nil, "Extra env var injected into the runner process, KEY=VALUE (repeatable)")
	cmd.Flags().Bool("clear-args", false, "Wipe stored extra args before applying any --arg values")
	cmd.Flags().Bool("clear-envs", false, "Wipe stored extra envs before applying any --env values")
	return cmd
}

func (c *DefaultCli) newAgentUpdateCommand() *cobra.Command {
	flags := config.ProvisionAgentUpdateFlags()

	cmd := &cobra.Command{
		Use:   "update <name>",
		Short: "Update a persisted agent's run config (args/envs) without launching a session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if c.operator == nil {
				return errors.New("operator is required")
			}
			resolved := flags
			if err := loadFlags(cmd, &resolved); err != nil {
				return err
			}
			envMap, err := parseEnvSlice(resolved.ExtraEnvs)
			if err != nil {
				return err
			}
			var runCfg *omniagent.RunConfig
			if len(resolved.ExtraArgs) > 0 || len(envMap) > 0 {
				runCfg = &omniagent.RunConfig{ExtraArgs: resolved.ExtraArgs, Envs: envMap}
			}
			if err := c.operator.UpdateAgent(operator.UpdateAgentParams{
				Workspace: sandbox.WorkspaceDir(resolved.Workspace),
				Name:      args[0],
				RunConfig: runCfg,
				ClearArgs: resolved.ClearArgs,
				ClearEnvs: resolved.ClearEnvs,
			}); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "agent %q updated\n", args[0])
			return nil
		},
	}

	cmd.Flags().String("workspace", flags.Workspace, "Workspace directory")
	cmd.Flags().StringArray("arg", nil, "Pass-through flag appended to the runner command line (repeatable)")
	cmd.Flags().StringArray("env", nil, "Extra env var injected into the runner process, KEY=VALUE (repeatable)")
	cmd.Flags().Bool("clear-args", false, "Wipe stored extra args before applying any --arg values")
	cmd.Flags().Bool("clear-envs", false, "Wipe stored extra envs before applying any --env values")
	return cmd
}

func (c *DefaultCli) newAgentUpgradeCommand() *cobra.Command {
	flags := config.ProvisionAgentUpgradeFlags()

	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade an agent memory template",
		RunE: func(cmd *cobra.Command, args []string) error {
			if c.operator == nil {
				return errors.New("operator is required")
			}
			resolved := flags
			if err := loadFlags(cmd, &resolved); err != nil {
				return err
			}
			if resolved.ID == "" {
				return errors.New("agent id is required")
			}
			return c.operator.UpgradeAgent(operator.UpgradeAgentParams{
				ID:      resolved.ID,
				Version: resolved.Version,
			})
		},
	}

	cmd.Flags().String("id", flags.ID, "Agent ID")
	cmd.Flags().String("version", flags.Version, "Target version (default: latest)")
	return cmd
}

func (c *DefaultCli) newAgentDeleteCommand() *cobra.Command {
	flags := config.ProvisionAgentDeleteFlags()

	deleteCmd := &cobra.Command{
		Use:   "delete [name]",
		Short: "Delete agent from index by id or name",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if c.operator == nil {
				return errors.New("operator is required")
			}
			resolved := flags
			if err := loadFlags(cmd, &resolved); err != nil {
				return err
			}
			if len(args) == 1 {
				resolved.Name = args[0]
			}
			if resolved.ID == "" {
				if resolved.Name == "" {
					return errors.New("agent id or name is required")
				}
				id, err := c.resolveAgentIDByName(resolved.Workspace, resolved.Name)
				if err != nil {
					return err
				}
				resolved.ID = id
			}
			_, _ = c.operator.StopSession(operator.StopSessionParams{
				AgentID: resolved.ID,
				Force:   true,
			})
			return c.operator.DeleteAgent(operator.DeleteAgentParams{ID: resolved.ID})
		},
	}

	deleteCmd.Flags().String("id", flags.ID, "Agent ID")
	deleteCmd.Flags().String("name", flags.Name, "Agent name (alternative to id)")
	deleteCmd.Flags().String("workspace", flags.Workspace, "Workspace directory (used with name)")

	return deleteCmd
}

func (c *DefaultCli) newAgentSwitchProviderCommand() *cobra.Command {
	flags := config.ProvisionAgentSwitchProviderFlags()
	var sessionID string

	switchCmd := &cobra.Command{
		Use:   "switch-provider [name]",
		Short: "Switch an agent to a different provider",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if c.operator == nil {
				return errors.New("operator is required")
			}
			resolved := flags
			if err := loadFlags(cmd, &resolved); err != nil {
				return err
			}
			if len(args) == 1 {
				resolved.Name = args[0]
			}
			if strings.TrimSpace(resolved.Provider) == "" {
				return errors.New("provider is required")
			}
			if resolved.ID == "" {
				if resolved.Name == "" {
					return errors.New("agent id or name is required")
				}
				id, err := c.resolveAgentIDByName(resolved.Workspace, resolved.Name)
				if err != nil {
					return err
				}
				resolved.ID = id
			}
			return c.operator.SwitchProvider(operator.SwitchProviderParams{
				ID:         resolved.ID,
				Provider:   codeagent.Provider(resolved.Provider),
				CleanStart: resolved.CleanStart,
				SessionID:  sessionID,
			})
		},
	}

	switchCmd.Flags().String("id", flags.ID, "Agent ID")
	switchCmd.Flags().String("name", flags.Name, "Agent name (alternative to id)")
	switchCmd.Flags().String("workspace", flags.Workspace, "Workspace directory (used with name)")
	switchCmd.Flags().StringP("provider", "p", flags.Provider, "Target provider")
	switchCmd.Flags().Bool("clean_start", flags.CleanStart, "Force a clean session instead of reusing an active one")
	switchCmd.Flags().StringVar(&sessionID, "session-id", "", "Optional session ID")

	return switchCmd
}

func (c *DefaultCli) newAgentPipeCommand() *cobra.Command {
	var prompt string
	var sessionID string

	cmd := &cobra.Command{
		Use:   "pipe <name>",
		Short: "Write raw data to an agent session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if c.operator == nil {
				return errors.New("operator is required")
			}
			if prompt == "" {
				return errors.New("prompt is required")
			}
			return c.operator.Pipe(operator.PipeParams{
				AgentID:   args[0],
				SessionID: sessionID,
				Data:      []byte(prompt),
			})
		},
	}

	cmd.Flags().StringVar(&prompt, "prompt", "", "Prompt data to write")
	cmd.Flags().StringVar(&sessionID, "session-id", "", "Optional session ID")
	_ = cmd.MarkFlagRequired("prompt")
	return cmd
}

func (c *DefaultCli) newAgentExecCommand() *cobra.Command {
	var prompt string
	var sessionID string
	var agentID string
	var bg bool

	cmd := &cobra.Command{
		Use:   "exec <name>",
		Short: "Send a prompt to an agent session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if c.operator == nil {
				return errors.New("operator is required")
			}
			if prompt == "" {
				return errors.New("prompt is required")
			}
			name := args[0]
			resolvedID := agentID
			if resolvedID == "" {
				id, err := c.resolveAgentIDByName(os.Getenv("OMNI_WORKSPACE"), name)
				if err != nil {
					return err
				}
				resolvedID = id
			}
			if bg {
				if err := c.operator.ResumeAgent(operator.ResumeAgentParams{
					Name:      name,
					SessionID: sessionID,
					Detached:  true,
				}); err != nil {
					return err
				}
			}
			if _, err := c.operator.ExecInSession(operator.ExecInSessionParams{
				AgentID:   resolvedID,
				SessionID: sessionID,
				Prompt:    prompt,
				Detached:  bg,
			}); err != nil {
				return err
			}
			fmt.Println("prompt sent")
			return nil
		},
	}

	cmd.Flags().StringVar(&prompt, "prompt", "", "Prompt to send")
	cmd.Flags().StringVar(&sessionID, "session-id", "", "Optional session ID")
	cmd.Flags().StringVar(&agentID, "id", "", "Agent UUID (bypasses name resolution)")
	cmd.Flags().BoolVarP(&bg, "bg", "b", false, "Resume agent in background before sending prompt")
	_ = cmd.MarkFlagRequired("prompt")
	return cmd
}

func (c *DefaultCli) newAgentStopCommand() *cobra.Command {
	var sessionID string
	var force bool

	cmd := &cobra.Command{
		Use:   "stop <name>",
		Short: "Stop an agent PTY session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if c.operator == nil {
				return errors.New("operator is required")
			}
			result, err := c.operator.StopSession(operator.StopSessionParams{
				AgentID:   args[0],
				SessionID: sessionID,
				Force:     force,
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "session stopped: %s\n", result.SessionID)
			return nil
		},
	}

	cmd.Flags().StringVar(&sessionID, "session-id", "", "Optional session ID")
	cmd.Flags().BoolVar(&force, "force", false, "Stop even when a client is attached")
	return cmd
}

func (c *DefaultCli) newAgentDetachCommand() *cobra.Command {
	var sessionID string

	cmd := &cobra.Command{
		Use:   "detach <name>",
		Short: "Detach an agent PTY session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if c.operator == nil {
				return errors.New("operator is required")
			}
			result, err := c.operator.DetachSession(operator.DetachSessionParams{
				AgentID:   args[0],
				SessionID: sessionID,
			})
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "session detached: %s\n", result.SessionID)
			return nil
		},
	}

	cmd.Flags().StringVar(&sessionID, "session-id", "", "Optional session ID")
	return cmd
}

func (c *DefaultCli) newAgentSandboxCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Manage sandbox runtime for an agent",
	}
	cmd.AddCommand(c.newAgentSandboxSyncCommand())
	return cmd
}

type agentSandboxSyncResult struct {
	AgentID     string `json:"agent_id" yaml:"agent_id"`
	Name        string `json:"name" yaml:"name"`
	Workspace   string `json:"workspace" yaml:"workspace"`
	Provider    string `json:"provider" yaml:"provider"`
	Provisioner string `json:"provisioner" yaml:"provisioner"`
	Created     bool   `json:"created" yaml:"created"`
	Synced      bool   `json:"synced" yaml:"synced"`
}

func (c *DefaultCli) newAgentSandboxSyncCommand() *cobra.Command {
	flags := config.ProvisionAgentSandboxSyncFlags()

	cmd := &cobra.Command{
		Use:   "sync [name]",
		Short: "Sync sandbox config for an agent runtime",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if c.operator == nil {
				return errors.New("operator is required")
			}
			resolved := flags
			if err := loadFlags(cmd, &resolved); err != nil {
				return err
			}
			if len(args) == 1 {
				resolved.Name = args[0]
			}
			if resolved.ID == "" && strings.TrimSpace(resolved.Name) == "" {
				return errors.New("agent id or name is required")
			}

			agent, err := c.resolveAgentInfo(resolved.Workspace, resolved.ID, resolved.Name)
			if err != nil {
				return err
			}

			provider := strings.TrimSpace(resolved.Provider)
			if provider == "" {
				provider = string(resolveActiveProvider(agent.ID))
			}

			kinds := omnisandbox.HostSupportedProvisioners()
			if len(kinds) == 0 {
				return errors.New("sandbox provisioner is not supported on this OS")
			}
			kind := kinds[0]

			provisioner, err := omnisandbox.NewProvisioner(kind, nil, omnisandbox.ProvisionerOptions{})
			if err != nil {
				return fmt.Errorf("init sandbox provisioner: %w", err)
			}

			cfg := defaults.SandboxConfig(codeagent.Provider(provider), string(agent.WorkspaceDir))
			pid := agent.ID
			rt, err := provisioner.GetSandbox(&omnisandbox.GetSandboxParams{PID: &pid})
			created := false
			if err != nil {
				rt, err = provisioner.Create(omnisandbox.CreateSandboxParams{
					ID:        agent.ID,
					ConfigDir: sandboxConfigDir(string(agent.WorkspaceDir), agent.Name),
					Config:    cfg,
				})
				if err != nil {
					return fmt.Errorf("create sandbox runtime: %w", err)
				}
				created = true
			}

			if err := rt.Sync(cfg); err != nil {
				return fmt.Errorf("sync sandbox runtime: %w", err)
			}

			result := agentSandboxSyncResult{
				AgentID:     agent.ID,
				Name:        agent.Name,
				Workspace:   string(agent.WorkspaceDir),
				Provider:    provider,
				Provisioner: string(kind),
				Created:     created,
				Synced:      true,
			}
			return printOutput("agent.sandbox.sync", resolved.Output, result)
		},
	}

	cmd.Flags().String("id", flags.ID, "Agent ID")
	cmd.Flags().String("name", flags.Name, "Agent name (alternative to id)")
	cmd.Flags().String("workspace", flags.Workspace, "Workspace directory")
	cmd.Flags().StringP("provider", "p", flags.Provider, "Provider used to resolve sandbox defaults")
	cmd.Flags().StringP("output", "o", flags.Output, "Output format: table|yaml|json")
	return cmd
}

func (c *DefaultCli) newAxoLinkCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "axolink",
		Short: "Start the axolink MCP service (stdio transport)",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := runner.DefaultConfig()
			cfg.Transport = runner.TransportStdio
			return runner.Run(cmd.Context(), cfg)
		},
	}
}

func (c *DefaultCli) resolveAgentIDByName(workspace, name string) (string, error) {
	agent, err := c.resolveAgentInfo(workspace, "", name)
	if err != nil {
		return "", err
	}
	return agent.ID, nil
}

func (c *DefaultCli) resolveAgentInfo(workspace, id, name string) (*omniagent.AgentInfo, error) {
	if c.operator == nil {
		return nil, errors.New("operator is required")
	}
	needleName := strings.TrimSpace(name)
	needleID := strings.TrimSpace(id)
	if needleName == "" && needleID == "" {
		return nil, errors.New("agent id or name is required")
	}
	result, err := c.operator.ListCodeAgents(operator.GetCodeAgentsParams{
		Workspace: sandbox.WorkspaceDir(workspace),
	})
	if err != nil {
		return nil, err
	}
	var matches []*omniagent.AgentInfo
	for _, a := range result.Agents {
		if a == nil {
			continue
		}
		if needleID != "" && strings.TrimSpace(a.ID) == needleID {
			matches = append(matches, a)
			continue
		}
		if needleName != "" && strings.TrimSpace(a.Name) == needleName {
			matches = append(matches, a)
		}
	}
	if len(matches) == 0 {
		if needleID != "" {
			return nil, fmt.Errorf("agent id %q not found", needleID)
		}
		return nil, fmt.Errorf("agent %q not found", needleName)
	}
	if len(matches) > 1 {
		if needleID != "" {
			return nil, fmt.Errorf("multiple agents with id %q found; use --id", needleID)
		}
		return nil, fmt.Errorf("multiple agents named %q found; use --id", needleName)
	}
	return matches[0], nil
}

func resolveActiveProvider(agentID string) codeagent.Provider {
	provider := codeagent.Provider(operator.DefaultProvider)
	sessionStore, err := codesession.GetCodeSessionStore()
	if err != nil || sessionStore == nil {
		return provider
	}
	session, err := sessionStore.GetSession(agentID)
	if err != nil || session == nil || session.Model == nil || session.Model.Provider == "" {
		return provider
	}
	return session.Model.Provider
}

func sandboxConfigDir(workspaceDir, agentName string) string {
	if strings.TrimSpace(workspaceDir) == "" || strings.TrimSpace(agentName) == "" {
		return ""
	}
	// Use version-aware resolution so v1/v2 agents (memory/agents/<name>/sandbox)
	// and v3 agents (memory/<name>/sandbox) are both handled correctly.
	return filepath.Join(operator.ResolveAgentMemDir(workspaceDir, agentName), "sandbox")
}

func serviceUnitName() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return "omni@" + u.Username
	}
	if v := os.Getenv("USER"); v != "" {
		return "omni@" + v
	}
	return "omni@" + os.Getenv("LOGNAME")
}

func runSystemctl(action string) error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return err
	}
	unit := serviceUnitName()
	return exec.Command("systemctl", action, unit).Run()
}

func printSystemctlStatus() error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		fmt.Println("server not running")
		return nil
	}
	cmd := exec.Command("systemctl", "status", serviceUnitName())
	out, _ := cmd.CombinedOutput() // non-zero exit is normal for stopped/failed states
	if len(out) == 0 {
		fmt.Println("server not running")
		return nil
	}
	fmt.Print(string(out))
	return nil
}

func fetchServerStatus(socketPath string) (*serverStatusResponse, error) {
	client := newUnixHTTPClient(socketPath)
	resp, err := client.Get("http://localhost/status")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("server status: %s", resp.Status)
	}
	var status serverStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return nil, err
	}
	return &status, nil
}

func ptyDaemonSocketPath() string {
	if path := os.Getenv("PTYDAEMON_SOCKET"); path != "" {
		return path
	}
	return "/tmp/ptydaemon.sock"
}

func newUnixHTTPClient(socketPath string) *http.Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}
	return &http.Client{Transport: transport}
}

func resolvePTYDaemonBinary() (string, error) {
	candidates := []string{
		filepath.Join("svc", "ptydaemon", "omni-server"),
		filepath.Join("svc", "ptydaemon", "cmd", "omni-server"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	if path, err := exec.LookPath("omni-server"); err == nil {
		return path, nil
	}
	return "", errors.New("omni-server binary not found")
}

func ptyDaemonPIDFile() string {
	if path := os.Getenv("PTYDAEMON_PID"); path != "" {
		return path
	}
	return filepath.Join(os.TempDir(), "omni-server.pid")
}

func loadFlags(cmd *cobra.Command, target any) error {
	k := koanf.New(".")
	if err := k.Load(posflag.Provider(cmd.Flags(), ".", k), nil); err != nil {
		return fmt.Errorf("load command flags: %w", err)
	}
	if err := k.Unmarshal("", target); err != nil {
		return fmt.Errorf("resolve command flags: %w", err)
	}
	return nil
}

// parseEnvSlice converts ["KEY=VALUE", ...] strings into a map[string]string.
// Returns an error on any entry that does not contain "=".
func parseEnvSlice(envs []string) (map[string]string, error) {
	if len(envs) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(envs))
	for _, e := range envs {
		idx := strings.IndexByte(e, '=')
		if idx <= 0 {
			return nil, fmt.Errorf("--env %q: expected KEY=VALUE format", e)
		}
		out[e[:idx]] = e[idx+1:]
	}
	return out, nil
}

func printOutput(kind, format string, v any) error {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "json":
		return printJSON(v)
	case "yaml", "yml":
		return printYAML(v)
	case "table":
		return printTable(kind, v)
	default:
		return fmt.Errorf("unsupported output format %q (use: yaml|table|json)", format)
	}
}

func printJSON(v any) error {
	payload, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(payload))
	return nil
}

func printYAML(v any) error {
	payload, err := yaml.Marshal(v)
	if err != nil {
		return err
	}
	fmt.Print(string(payload))
	return nil
}

func printTable(kind string, v any) error {
	switch kind {
	case "config.get":
		return printConfigTable(v)
	case "agent.discover":
		return printDiscoverTable(v)
	case "agent.list":
		return printAgentListTable(v)
	case "agent.sandbox.sync":
		return printAgentSandboxSyncTable(v)
	case "team.get":
		return printWorkspaceTable(v)
	case "team.list":
		return printTeamListTable(v)
	case "doctor.check", "doctor.install":
		return printDoctorTable(v)
	default:
		return printJSON(v)
	}
}

func printConfigTable(v any) error {
	cfg, ok := v.(*config.OmniConfig)
	if !ok || cfg == nil {
		return errors.New("invalid config payload for table output")
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "KEY\tVALUE")
	memory := false
	autoSync := false
	if cfg.Features != nil {
		memory = cfg.Features.Memory
		autoSync = cfg.Features.AutoSync
	}
	fmt.Fprintf(tw, "features.memory\t%t\n", memory)
	fmt.Fprintf(tw, "features.autosync\t%t\n", autoSync)
	fmt.Fprintf(tw, "agent\t%t\n", cfg.Agent != nil)
	return tw.Flush()
}

func printDiscoverTable(v any) error {
	result, ok := v.(*operator.DisocveryResult)
	if !ok || result == nil {
		return errors.New("invalid discover payload for table output")
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "PROVIDER")
	for _, provider := range result.Providers {
		fmt.Fprintf(tw, "%s\n", provider)
	}
	return tw.Flush()
}

func printAgentListTable(v any) error {
	result, ok := v.(*operator.GetAgentsResult)
	if !ok || result == nil {
		return errors.New("invalid agent list payload for table output")
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT_ID\tNAME\tWORKSPACE\tMEMORY_DIR")
	for _, a := range result.Agents {
		if a == nil {
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", a.ID, a.Name, a.WorkspaceDir, a.MemoryDir)
	}
	return tw.Flush()
}

func printWorkspaceTable(v any) error {
	result, ok := v.(operator.GetTeamResult)
	if !ok {
		return errors.New("invalid workspace payload for table output")
	}

	if result.Info != nil {
		fmt.Printf("WORKSPACE\t%s\n", result.Info.ID)
		fmt.Printf("NAME\t%s\n", result.Info.Name)
		fmt.Printf("DIR\t%s\n", result.Info.WorkspaceDir)
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT_ID\tNAME\tWORKSPACE\tMEMORY_DIR")
	for _, a := range result.Agents {
		if a == nil {
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", a.ID, a.Name, a.WorkspaceDir, a.MemoryDir)
	}
	return tw.Flush()
}

func printTeamListTable(v any) error {
	result, ok := v.(operator.ListWorkspacesResult)
	if !ok {
		return errors.New("invalid team list payload for table output")
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "TEAM_ID\tNAME\tWORKSPACE_DIR\tAGENTS")
	for _, t := range result.Teams {
		if t == nil {
			continue
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%d\n", t.ID, t.Name, t.WorkspaceDir, t.Agents)
	}
	return tw.Flush()
}

func printDoctorTable(v any) error {
	status, ok := v.(omnisandbox.HealthStatus)
	if !ok {
		return errors.New("invalid doctor payload for table output")
	}

	state := "OK"
	next := status.Next
	if strings.TrimSpace(next) == "" {
		next = "-"
	}
	if !status.Installed {
		state = "TODO"
		if strings.TrimSpace(next) == "-" {
			switch status.Provider {
			case omnisandbox.ProvisionerGVisor:
				next = "run: omni doctor install"
			case omnisandbox.ProvisionerSeatbelt:
				next = "install/enable sandbox-exec"
			default:
				next = "unsupported OS/runtime"
			}
		}
	}
	missing := "-"
	if len(status.Missing) > 0 {
		missing = strings.Join(status.Missing, ",")
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tOS\tPROVIDER\tINSTALLED\tBINARY\tMISSING\tNEXT")
	fmt.Fprintf(tw, "%s\t%s\t%s\t%t\t%s\t%s\t%s\n", state, status.OS, status.Provider, status.Installed, status.Binary, missing, next)
	return tw.Flush()
}

func printAgentSandboxSyncTable(v any) error {
	result, ok := v.(agentSandboxSyncResult)
	if !ok {
		return errors.New("invalid agent sandbox sync payload for table output")
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT_ID\tNAME\tWORKSPACE\tPROVIDER\tPROVISIONER\tCREATED\tSYNCED")
	fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%t\t%t\n",
		result.AgentID, result.Name, result.Workspace, result.Provider, result.Provisioner, result.Created, result.Synced)
	return tw.Flush()
}
