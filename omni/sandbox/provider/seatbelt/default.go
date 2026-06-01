package seatbelt

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	sandboxcommon "github.com/Shaik-Sirajuddin/omni/sandbox/common"
	"github.com/Shaik-Sirajuddin/omni/sandbox/provider"
)

type Provisioner struct {
	state   provider.ProvisionerState
	sandbox *provider.Sandbox
	options provider.ProvisionerOptions
	parser  provider.SandboxConfigParser
}

type Runtime struct {
	sandbox     *provider.Sandbox
	provisioner *Provisioner
}

func New(sbx *provider.Sandbox, opts provider.ProvisionerOptions) (*Provisioner, error) {
	return &Provisioner{
		state:   provider.NewProvisionerState(opts.Store),
		sandbox: provider.CloneSandbox(sbx),
		options: opts,
		parser:  seatbeltConfigParser{},
	}, nil
}

func (p *Provisioner) Create(params provider.CreateSandboxParams) (provider.SandboxRuntime, error) {
	cfg := params.Config
	if cfg == nil && p.sandbox != nil {
		cfg = p.sandbox.Config
	}
	resolvedCfg, _, err := sandboxcommon.EnsureCommonConfig(params.ConfigDir, p.options.ConfigParser, cfg)
	if err != nil {
		return nil, err
	}
	cfg = resolvedCfg
	params.Config = cfg
	if _, err := p.parseConfig(cfg); err != nil {
		return nil, err
	}
	if err := p.syncProvisionConfig(params.ConfigDir, cfg); err != nil {
		return nil, err
	}
	sbx, err := p.state.Create(provider.ProvisionerSeatbelt, params, p.sandbox)
	if err != nil {
		return nil, err
	}
	return p.wrap(sbx), nil
}

func (p *Provisioner) UpdateSandbox(params *provider.UpdateSandboxParams) (provider.SandboxRuntime, error) {
	if params == nil {
		return nil, fmt.Errorf("sandbox: update sandbox params are required")
	}
	id := strings.TrimSpace(params.ID)
	if id == "" {
		return nil, fmt.Errorf("sandbox: update sandbox id is required")
	}
	if _, err := p.parseConfig(params.Config); err != nil {
		return nil, err
	}
	name := id
	rt, err := p.GetSandbox(&provider.GetSandboxParams{Name: &name, Active: false})
	if err != nil {
		return nil, err
	}
	if err := rt.Sync(params.Config); err != nil {
		return nil, err
	}
	return rt, nil
}

func (p *Provisioner) List(params provider.ListSandboxParams) ([]provider.SandboxRuntime, error) {
	items, err := p.state.List(params)
	if err != nil {
		return nil, err
	}
	out := make([]provider.SandboxRuntime, 0, len(items))
	for _, sbx := range items {
		out = append(out, p.wrap(sbx))
	}
	return out, nil
}
func (p *Provisioner) GetSandbox(params *provider.GetSandboxParams) (provider.SandboxRuntime, error) {
	sbx, err := p.state.Get(params)
	if err != nil {
		return nil, err
	}
	return p.wrap(sbx), nil
}

func (p *Provisioner) wrap(sbx *provider.Sandbox) *Runtime {
	return &Runtime{sandbox: provider.CloneSandbox(sbx), provisioner: p}
}

func (r *Runtime) Sandbox() *provider.Sandbox { return provider.CloneSandbox(r.sandbox) }
func (r *Runtime) Command(command string, args []string) error {
	return r.provisioner.run(r.sandbox, command, args, true)
}
func (r *Runtime) Execute(command string, args []string) error {
	return r.provisioner.run(r.sandbox, command, args, false)
}
func (r *Runtime) Capture(command string, args []string) (*provider.ExecutionResult, error) {
	return r.provisioner.capture(r.sandbox, command, args)
}
func (r *Runtime) Start(command string, args []string) (provider.SandboxProcess, error) {
	return r.provisioner.start(r.sandbox, command, args)
}
func (r *Runtime) Sync(config *provider.Config) error {
	if r.sandbox == nil || r.sandbox.Data == nil {
		return fmt.Errorf("sandbox: runtime sandbox is required")
	}
	configDir := ""
	if r.sandbox != nil && r.sandbox.Data != nil {
		configDir = r.sandbox.Data.ConfigDir
	}
	syncedConfig := provider.CloneConfig(config)
	if strings.TrimSpace(configDir) != "" {
		if r.provisioner.options.ConfigParser == nil {
			return fmt.Errorf("sandbox: config parser is required for config dir sync")
		}
		updatedConfig, _, err := sandboxcommon.SyncCommonConfig(configDir, r.provisioner.options.ConfigParser, config)
		if err != nil {
			return err
		}
		syncedConfig = updatedConfig
	}
	if err := r.provisioner.syncProvisionConfig(configDir, syncedConfig); err != nil {
		return err
	}
	r.sandbox.Config = provider.CloneConfig(syncedConfig)
	r.provisioner.state.SyncOne(r.sandbox.Data.ID, syncedConfig)
	return nil
}

func (p *Provisioner) BuildCommand(sbx *provider.Sandbox, command string, args []string) (string, []string, error) {
	if strings.TrimSpace(command) == "" {
		return "", nil, fmt.Errorf("sandbox: command is required")
	}
	executable := p.options.Executable
	if executable == "" {
		executable = "sandbox-exec"
	}
	profile := p.profile(sbx)
	cmdArgs := []string{"-p", profile}
	cmdArgs = append(cmdArgs, p.options.ExtraArgs...)
	cmdArgs = append(cmdArgs, command)
	cmdArgs = append(cmdArgs, args...)
	return executable, cmdArgs, nil
}

func (p *Provisioner) run(sbx *provider.Sandbox, command string, args []string, interactive bool) error {
	cmd, err := p.command(sbx, command, args)
	if err != nil {
		return err
	}
	if interactive {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	return cmd.Run()
}

func (p *Provisioner) capture(sbx *provider.Sandbox, command string, args []string) (*provider.ExecutionResult, error) {
	cmd, err := p.command(sbx, command, args)
	if err != nil {
		return nil, err
	}
	return provider.RunCaptured(cmd)
}

func (p *Provisioner) start(sbx *provider.Sandbox, command string, args []string) (provider.SandboxProcess, error) {
	cmd, err := p.command(sbx, command, args)
	if err != nil {
		return nil, err
	}
	return provider.StartCaptured(cmd)
}

func (p *Provisioner) command(sbx *provider.Sandbox, command string, args []string) (*exec.Cmd, error) {
	executable, cmdArgs, err := p.BuildCommand(sbx, command, args)
	if err != nil {
		return nil, err
	}
	resolved, err := exec.LookPath(executable)
	if err != nil {
		return nil, fmt.Errorf("sandbox: resolve %s: %w", executable, err)
	}
	return exec.Command(resolved, cmdArgs...), nil
}

func (p *Provisioner) profile(sbx *provider.Sandbox) string {
	var rules []string
	rules = append(rules, "(version 1)", "(deny default)", "(allow process-exec)", "(allow sysctl-read)")
	workDir := p.options.WorkDir
	if workDir == "" {
		workDir = "."
	}
	workDir = filepath.Clean(workDir)
	readWriteRule := "(allow file-write* (subpath %q))"
	readRule := "(allow file-read* (subpath %q))"
	if provider.SandboxAllowsWrite(sbx) {
		rules = append(rules, fmt.Sprintf(readWriteRule, workDir))
	} else {
		rules = append(rules, fmt.Sprintf(readRule, workDir))
	}
	for _, dir := range provider.SandboxAccessDirs(sbx) {
		rules = append(rules, fmt.Sprintf(readRule, dir))
	}
	for _, dir := range provider.SandboxBlockedDirs(sbx) {
		rules = append(rules, fmt.Sprintf("(deny file-read* file-write* (subpath %q))", dir))
	}
	return strings.Join(rules, " ")
}

func (p *Provisioner) CreateDir(path string) error {
	target, err := p.resolveDir(path)
	if err != nil {
		return err
	}
	return os.MkdirAll(target, 0o755)
}

func (p *Provisioner) ListDirs(path string) ([]string, error) {
	target, err := p.resolveDir(path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			out = append(out, entry.Name())
		}
	}
	return out, nil
}

func (p *Provisioner) resolveDir(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("sandbox: directory path is required")
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	workDir := strings.TrimSpace(p.options.WorkDir)
	if workDir == "" {
		return "", fmt.Errorf("sandbox: relative directory path requires provisioner WorkDir")
	}
	return filepath.Join(filepath.Clean(workDir), path), nil
}

type seatbeltConfigParser struct{}

func (seatbeltConfigParser) Parse(config *provider.Config) (*provider.ParsedSandboxConfig, error) {
	sbx := &provider.Sandbox{Config: provider.CloneConfig(config)}
	return &provider.ParsedSandboxConfig{
		AllowWrite:  provider.SandboxAllowsWrite(sbx),
		AccessDirs:  provider.SandboxAccessDirs(sbx),
		BlockedDirs: provider.SandboxBlockedDirs(sbx),
	}, nil
}

func (p *Provisioner) parseConfig(config *provider.Config) (*provider.ParsedSandboxConfig, error) {
	if p.parser == nil {
		return nil, fmt.Errorf("sandbox: config parser is required")
	}
	return p.parser.Parse(config)
}

func (p *Provisioner) syncProvisionConfig(configDir string, config *provider.Config) error {
	rt := &provider.Sandbox{Config: provider.CloneConfig(config)}
	profile := p.profile(rt)
	_, err := sandboxcommon.WriteProviderTemplate(configDir, "seatbelt.profile.sb", []byte(profile+"\n"))
	return err
}
