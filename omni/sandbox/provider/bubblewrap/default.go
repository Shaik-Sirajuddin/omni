package bubblewrap

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	sandboxlog "github.com/Shaik-Sirajuddin/omni/sandbox/log"
	"github.com/Shaik-Sirajuddin/omni/sandbox/provider"
)

var logger = sandboxlog.NewLogger("bubblewrap")

type Provisioner struct {
	state       provider.ProvisionerState
	sandbox     *provider.Sandbox
	options     provider.ProvisionerOptions
	transformer ConfigTransformer
	store       Store
}

type Runtime struct {
	sandbox     *provider.Sandbox
	provisioner *Provisioner
}

func New(sbx *provider.Sandbox, opts provider.ProvisionerOptions) (*Provisioner, error) {
	logger.Debug("provisioner init", "hasSandbox", sbx != nil, "executable", opts.Executable, "workDir", opts.WorkDir, "extraArgs", opts.ExtraArgs)
	return &Provisioner{
		state:       provider.NewProvisionerState(opts.Store),
		sandbox:     provider.CloneSandbox(sbx),
		options:     opts,
		transformer: defaultConfigTransformer{},
		store:       NewStore(),
	}, nil
}

func (p *Provisioner) Create(params provider.CreateSandboxParams) (provider.SandboxRuntime, error) {
	logger.Debug("create runtime requested", "id", params.ID)
	sbx, err := p.state.Create(provider.ProvisionerBubblewrap, params, p.sandbox)
	if err != nil {
		logger.Error("create runtime failed", "id", params.ID, "err", err)
		return nil, err
	}
	logger.Info("runtime created", "id", params.ID)
	return p.wrap(sbx), nil
}

func (p *Provisioner) List(params provider.ListSandboxParams) ([]provider.SandboxRuntime, error) {
	logger.Debug("list runtimes requested", "activeOnly", params.Active)
	items, err := p.state.List(params)
	if err != nil {
		logger.Error("list runtimes failed", "err", err)
		return nil, err
	}
	out := make([]provider.SandboxRuntime, 0, len(items))
	for _, sbx := range items {
		out = append(out, p.wrap(sbx))
	}
	logger.Info("list runtimes completed", "count", len(out))
	return out, nil
}

func (p *Provisioner) UpdateSandbox(params *provider.UpdateSandboxParams) (provider.SandboxRuntime, error) {
	logger.Debug("update sandbox requested")
	if params == nil {
		logger.Error("update sandbox failed", "err", "params missing")
		return nil, fmt.Errorf("sandbox: update sandbox params are required")
	}
	id := strings.TrimSpace(params.ID)
	if id == "" {
		logger.Error("update sandbox failed", "err", "id missing")
		return nil, fmt.Errorf("sandbox: update sandbox id is required")
	}
	if _, err := p.parseConfig(params.Config); err != nil {
		logger.Error("update sandbox config parse failed", "id", id, "err", err)
		return nil, err
	}
	name := id
	rt, err := p.GetSandbox(&provider.GetSandboxParams{Name: &name, Active: false})
	if err != nil {
		logger.Error("update sandbox get failed", "id", id, "err", err)
		return nil, err
	}
	if err := rt.Sync(params.Config); err != nil {
		logger.Error("update sandbox sync failed", "id", id, "err", err)
		return nil, err
	}
	logger.Info("update sandbox completed", "id", id)
	return rt, nil
}

func (p *Provisioner) GetSandbox(params *provider.GetSandboxParams) (provider.SandboxRuntime, error) {
	logger.Debug("get runtime requested")
	sbx, err := p.state.Get(params)
	if err != nil {
		logger.Error("get runtime failed", "err", err)
		return nil, err
	}
	id := ""
	if sbx != nil && sbx.Data != nil {
		id = sbx.Data.ID
	}
	logger.Info("runtime loaded", "id", id)
	return p.wrap(sbx), nil
}

func (p *Provisioner) wrap(sbx *provider.Sandbox) *Runtime {
	return &Runtime{sandbox: provider.CloneSandbox(sbx), provisioner: p}
}

func (r *Runtime) Sandbox() *provider.Sandbox { return provider.CloneSandbox(r.sandbox) }
func (r *Runtime) Command(command string, args []string) error {
	logger.Debug("runtime command", "id", runtimeID(r.sandbox), "command", command, "args", args, "interactive", true)
	return r.provisioner.run(r.sandbox, command, args, true)
}
func (r *Runtime) Execute(command string, args []string) error {
	logger.Debug("runtime execute", "id", runtimeID(r.sandbox), "command", command, "args", args, "interactive", false)
	return r.provisioner.run(r.sandbox, command, args, false)
}
func (r *Runtime) Capture(command string, args []string) (*provider.ExecutionResult, error) {
	logger.Debug("runtime capture", "id", runtimeID(r.sandbox), "command", command, "args", args)
	return r.provisioner.capture(r.sandbox, command, args)
}
func (r *Runtime) Start(command string, args []string) (provider.SandboxProcess, error) {
	logger.Debug("runtime start", "id", runtimeID(r.sandbox), "command", command, "args", args)
	return r.provisioner.start(r.sandbox, command, args)
}
func (r *Runtime) Sync(config *provider.Config) error {
	if r.sandbox == nil || r.sandbox.Data == nil {
		logger.Error("runtime sync failed", "err", "runtime sandbox missing")
		return fmt.Errorf("sandbox: runtime sandbox is required")
	}
	logger.Debug("runtime sync requested", "id", r.sandbox.Data.ID)
	r.sandbox.Config = provider.CloneConfig(config)
	r.provisioner.state.SyncOne(r.sandbox.Data.ID, config)
	logger.Info("runtime sync completed", "id", r.sandbox.Data.ID)
	return nil
}

func (p *Provisioner) TransformFromSandbox(config *provider.Config) (*Config, error) {
	if p.transformer == nil {
		logger.Error("transform from sandbox failed", "err", "transformer missing")
		return nil, fmt.Errorf("sandbox: bubblewrap config transformer is required")
	}
	logger.Debug("transform from sandbox requested")
	return p.transformer.FromSandbox(config, p.options)
}

func (p *Provisioner) TransformToSandbox(config *Config) (*provider.Config, error) {
	if p.transformer == nil {
		logger.Error("transform to sandbox failed", "err", "transformer missing")
		return nil, fmt.Errorf("sandbox: bubblewrap config transformer is required")
	}
	logger.Debug("transform to sandbox requested")
	return p.transformer.ToSandbox(config)
}

func (p *Provisioner) LoadConfig(path string) (*Config, error) {
	if p.store == nil {
		logger.Error("load config failed", "path", path, "err", "store missing")
		return nil, fmt.Errorf("sandbox: bubblewrap config store is required")
	}
	logger.Debug("load config requested", "path", path)
	return p.store.Load(path)
}

func (p *Provisioner) SaveConfig(path string, config *Config) error {
	if p.store == nil {
		logger.Error("save config failed", "path", path, "err", "store missing")
		return fmt.Errorf("sandbox: bubblewrap config store is required")
	}
	logger.Debug("save config requested", "path", path)
	return p.store.Save(path, config)
}

func (p *Provisioner) parseConfig(config *provider.Config) (*Config, error) {
	return p.TransformFromSandbox(config)
}

func (p *Provisioner) BuildCommand(sbx *provider.Sandbox, command string, args []string) (string, []string, error) {
	if strings.TrimSpace(command) == "" {
		logger.Error("build command failed", "err", "command missing")
		return "", nil, fmt.Errorf("sandbox: command is required")
	}
	executable := p.options.Executable
	if executable == "" {
		executable = "bwrap"
	}
	workDir := p.options.WorkDir
	if workDir == "" {
		workDir = "."
	}
	resolvedWorkDir := filepath.Clean(workDir)
	cmdArgs := []string{"--die-with-parent", "--new-session", "--unshare-pid", "--proc", "/proc", "--dev", "/dev", "--ro-bind", "/", "/"}
	if provider.SandboxAllowsWrite(sbx) {
		cmdArgs = append(cmdArgs, "--bind", resolvedWorkDir, resolvedWorkDir)
	} else {
		cmdArgs = append(cmdArgs, "--ro-bind", resolvedWorkDir, resolvedWorkDir)
	}
	for _, dir := range provider.SandboxAccessDirs(sbx) {
		cmdArgs = append(cmdArgs, "--ro-bind", dir, dir)
	}
	for _, dir := range provider.SandboxBlockedDirs(sbx) {
		cmdArgs = append(cmdArgs, "--tmpfs", dir)
	}
	cmdArgs = append(cmdArgs, "--chdir", resolvedWorkDir)
	cmdArgs = append(cmdArgs, p.options.ExtraArgs...)
	cmdArgs = append(cmdArgs, "--", command)
	cmdArgs = append(cmdArgs, args...)
	logger.Debug("command built", "executable", executable, "args", cmdArgs)
	return executable, cmdArgs, nil
}

func (p *Provisioner) run(sbx *provider.Sandbox, command string, args []string, interactive bool) error {
	cmd, err := p.command(sbx, command, args)
	if err != nil {
		logger.Error("run command build failed", "command", command, "args", args, "err", err)
		return err
	}
	if interactive {
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	if err := cmd.Run(); err != nil {
		logger.Error("run failed", "command", command, "args", args, "interactive", interactive, "err", err)
		return err
	}
	logger.Info("run completed", "command", command, "args", args, "interactive", interactive)
	return nil
}

func (p *Provisioner) capture(sbx *provider.Sandbox, command string, args []string) (*provider.ExecutionResult, error) {
	cmd, err := p.command(sbx, command, args)
	if err != nil {
		logger.Error("capture command build failed", "command", command, "args", args, "err", err)
		return nil, err
	}
	out, err := provider.RunCaptured(cmd)
	if err != nil {
		logger.Error("capture failed", "command", command, "args", args, "err", err)
		return nil, err
	}
	logger.Info("capture completed", "command", command, "args", args, "exitCode", out.ExitCode)
	return out, nil
}

func (p *Provisioner) start(sbx *provider.Sandbox, command string, args []string) (provider.SandboxProcess, error) {
	cmd, err := p.command(sbx, command, args)
	if err != nil {
		logger.Error("start command build failed", "command", command, "args", args, "err", err)
		return nil, err
	}
	process, err := provider.StartCaptured(cmd)
	if err != nil {
		logger.Error("start failed", "command", command, "args", args, "err", err)
		return nil, err
	}
	logger.Info("start completed", "command", command, "args", args, "pid", process.PID())
	return process, nil
}

func (p *Provisioner) command(sbx *provider.Sandbox, command string, args []string) (*exec.Cmd, error) {
	executable, cmdArgs, err := p.BuildCommand(sbx, command, args)
	if err != nil {
		return nil, err
	}
	resolved, err := exec.LookPath(executable)
	if err != nil {
		logger.Error("resolve executable failed", "executable", executable, "err", err)
		return nil, fmt.Errorf("sandbox: resolve %s: %w", executable, err)
	}
	logger.Debug("command resolved", "requested", executable, "resolved", resolved)
	return exec.Command(resolved, cmdArgs...), nil
}

func (p *Provisioner) CreateDir(path string) error {
	target, err := p.resolveDir(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(target, 0o755); err != nil {
		logger.Error("create dir failed", "path", path, "resolved", target, "err", err)
		return err
	}
	logger.Info("create dir completed", "path", path, "resolved", target)
	return nil
}

func (p *Provisioner) ListDirs(path string) ([]string, error) {
	target, err := p.resolveDir(path)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		logger.Error("list dirs failed", "path", path, "resolved", target, "err", err)
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			out = append(out, entry.Name())
		}
	}
	logger.Info("list dirs completed", "path", path, "resolved", target, "count", len(out))
	return out, nil
}

func (p *Provisioner) resolveDir(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		logger.Error("resolve dir failed", "err", "path missing")
		return "", fmt.Errorf("sandbox: directory path is required")
	}
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	workDir := strings.TrimSpace(p.options.WorkDir)
	if workDir == "" {
		logger.Error("resolve dir failed", "path", path, "err", "workdir missing")
		return "", fmt.Errorf("sandbox: relative directory path requires provisioner WorkDir")
	}
	return filepath.Join(filepath.Clean(workDir), path), nil
}

func runtimeID(sbx *provider.Sandbox) string {
	if sbx == nil || sbx.Data == nil {
		return ""
	}
	return sbx.Data.ID
}
