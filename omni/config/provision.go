package config

// CLIConfigGetFlags is a koanf-compatible flag schema for `config get`.
type CLIConfigGetFlags struct {
	Output string `koanf:"output" json:"output"`
}

// CLITeamListFlags is a koanf-compatible flag schema for `team list`.
type CLITeamListFlags struct {
	Output string `koanf:"output" json:"output"`
}

// CLITeamInitFlags is a koanf-compatible flag schema for `team init`.
type CLITeamInitFlags struct {
	RepoURL        string `koanf:"repo_url" json:"repo_url"`
	Layout         string `koanf:"layout" json:"layout"`
	TerminalLayout string `koanf:"t-layout" json:"t_layout"`
	Terminal       string `koanf:"terminal" json:"terminal"`
}

// CLIDoctorTerminalFlags is a koanf-compatible flag schema for `doctor terminal check/install`.
type CLIDoctorTerminalFlags struct {
	Output string `koanf:"output" json:"output"`
	Name   string `koanf:"name" json:"name"`
}

// CLITeamGetFlags is a koanf-compatible flag schema for `team get`.
type CLITeamGetFlags struct {
	ID     string `koanf:"id" json:"id"`
	Output string `koanf:"output" json:"output"`
}

// CLIConfigSetFlags is a koanf-compatible flag schema for `config set`.
// Empty string means "do not update this field".
type CLIConfigSetFlags struct {
	Memory   string `koanf:"memory" json:"memory"`
	AutoSync string `koanf:"autosync" json:"autosync"`
	Theme    string `koanf:"theme" json:"theme"`
}

// CLIAgentListFlags is a koanf-compatible flag schema for `agent list`.
type CLIAgentListFlags struct {
	Workspace string `koanf:"workspace" json:"workspace"`
	Output    string `koanf:"output" json:"output"`
}

// CLIAgentCreateFlags is a koanf-compatible flag schema for `agent init`.
type CLIAgentCreateFlags struct {
	Workspace          string `koanf:"workspace" json:"workspace"`
	Name               string `koanf:"name" json:"name"`
	Provider           string `koanf:"provider" json:"provider"`
	Model              string `koanf:"model" json:"model"`
	AllowGeneratedName bool   `koanf:"allow_generated_name" json:"allow_generated_name"`
	ResumeIfExists     bool   `koanf:"resume_if_exists" json:"resume_if_exists"`
	Interactive        bool   `koanf:"interactive" json:"interactive"`
}

// CLIAgentDeleteFlags is a koanf-compatible flag schema for `agent delete`.
type CLIAgentDeleteFlags struct {
	ID        string `koanf:"id" json:"id"`
	Name      string `koanf:"name" json:"name"`
	Workspace string `koanf:"workspace" json:"workspace"`
}

// CLIAgentDiscoverFlags is a koanf-compatible flag schema for `agent discover`.
type CLIAgentDiscoverFlags struct {
	Output string `koanf:"output" json:"output"`
}

// CLIAgentSwitchProviderFlags is a koanf-compatible flag schema for `agent switch-provider`.
type CLIAgentSwitchProviderFlags struct {
	ID         string `koanf:"id" json:"id"`
	Name       string `koanf:"name" json:"name"`
	Workspace  string `koanf:"workspace" json:"workspace"`
	Provider   string `koanf:"provider" json:"provider"`
	CleanStart bool   `koanf:"clean_start" json:"clean_start"`
}

// CLIAgentSandboxSyncFlags is a koanf-compatible flag schema for `agent sandbox sync`.
type CLIAgentSandboxSyncFlags struct {
	ID        string `koanf:"id" json:"id"`
	Name      string `koanf:"name" json:"name"`
	Workspace string `koanf:"workspace" json:"workspace"`
	Provider  string `koanf:"provider" json:"provider"`
	Output    string `koanf:"output" json:"output"`
}

// CLIAgentResumeFlags is a koanf-compatible flag schema for `agent resume`.
type CLIAgentResumeFlags struct {
	Workspace     string `koanf:"workspace" json:"workspace"`
	InitIfMissing bool   `koanf:"init_if_missing" json:"init_if_missing"`
	Provider      string `koanf:"provider" json:"provider"`
	Model         string `koanf:"model" json:"model"`
}

// CLIAgentUpgradeFlags is a koanf-compatible flag schema for `agent upgrade`.
type CLIAgentUpgradeFlags struct {
	ID      string `koanf:"id" json:"id"`
	Version string `koanf:"version" json:"version"`
}

// CLIDoctorCheckFlags is a koanf-compatible flag schema for `doctor check`.
type CLIDoctorCheckFlags struct {
	Output string `koanf:"output" json:"output"`
}

// CLIDoctorInstallFlags is a koanf-compatible flag schema for `doctor install`.
type CLIDoctorInstallFlags struct {
	Output string `koanf:"output" json:"output"`
}

func ProvisionConfigGetFlags() CLIConfigGetFlags {
	return CLIConfigGetFlags{Output: "yaml"}
}

func ProvisionTeamListFlags() CLITeamListFlags {
	return CLITeamListFlags{Output: "table"}
}

func ProvisionTeamInitFlags() CLITeamInitFlags {
	return CLITeamInitFlags{Terminal: "zellij"}
}

func ProvisionDoctorTerminalFlags() CLIDoctorTerminalFlags {
	return CLIDoctorTerminalFlags{Output: "table"}
}

func ProvisionTeamGetFlags() CLITeamGetFlags {
	return CLITeamGetFlags{Output: "table"}
}

func ProvisionConfigSetFlags() CLIConfigSetFlags {
	return CLIConfigSetFlags{}
}

func ProvisionAgentListFlags() CLIAgentListFlags {
	return CLIAgentListFlags{Output: "table"}
}

func ProvisionAgentCreateFlags() CLIAgentCreateFlags {
	return CLIAgentCreateFlags{
		Interactive: true,
	}
}

func ProvisionAgentDeleteFlags() CLIAgentDeleteFlags {
	return CLIAgentDeleteFlags{}
}

func ProvisionAgentSwitchProviderFlags() CLIAgentSwitchProviderFlags {
	return CLIAgentSwitchProviderFlags{}
}

func ProvisionAgentSandboxSyncFlags() CLIAgentSandboxSyncFlags {
	return CLIAgentSandboxSyncFlags{Output: "table"}
}

func ProvisionAgentDiscoverFlags() CLIAgentDiscoverFlags {
	return CLIAgentDiscoverFlags{Output: "table"}
}

func ProvisionAgentResumeFlags() CLIAgentResumeFlags {
	return CLIAgentResumeFlags{}
}

func ProvisionAgentUpgradeFlags() CLIAgentUpgradeFlags {
	return CLIAgentUpgradeFlags{}
}

func ProvisionDoctorCheckFlags() CLIDoctorCheckFlags {
	return CLIDoctorCheckFlags{Output: "table"}
}

func ProvisionDoctorInstallFlags() CLIDoctorInstallFlags {
	return CLIDoctorInstallFlags{Output: "table"}
}

// ProvisionDefaultOmniConfig returns a new default OmniConfig instance.
func ProvisionDefaultOmniConfig() *OmniConfig {
	return &OmniConfig{
		Features: &Features{
			AutoSync:         true,
			RandomAgentNames: true,
		},
	}
}

// ApplyOmniConfigDefaults ensures optional OmniConfig sections are initialized.
func ApplyOmniConfigDefaults(cfg *OmniConfig) *OmniConfig {
	if cfg == nil {
		return ProvisionDefaultOmniConfig()
	}

	if cfg.Features == nil {
		cfg.Features = &Features{
			AutoSync:         true,
			RandomAgentNames: true,
		}
	}

	return cfg
}
