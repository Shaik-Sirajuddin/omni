package gvisor

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	sandboxcommon "github.com/Shaik-Sirajuddin/omni/sandbox/common"
	sandboxlog "github.com/Shaik-Sirajuddin/omni/sandbox/log"
	"github.com/Shaik-Sirajuddin/omni/sandbox/provider"
	"github.com/adrg/xdg"
)

var logger = sandboxlog.NewLogger("gvisor")

var runscNeedsIgnoreCgroupsFn = runscNeedsIgnoreCgroups

type Provisioner struct {
	state   provider.ProvisionerState
	sandbox *provider.Sandbox
	options provider.ProvisionerOptions
	ConfigTransformer
}

type Runtime struct {
	sandbox     *provider.Sandbox
	provisioner *Provisioner
}

func New(sbx *provider.Sandbox, opts provider.ProvisionerOptions) (*Provisioner, error) {
	logger.Debug("provisioner init", "hasSandbox", sbx != nil, "executable", opts.Executable, "globalArgs", opts.GlobalArgs, "extraArgs", opts.ExtraArgs)
	p := &Provisioner{
		state:   provider.NewProvisionerState(opts.Store),
		sandbox: provider.CloneSandbox(sbx),
		options: opts,
	}
	p.ConfigTransformer = &defaultConfigTransformer{}
	return p, nil
}

func (p *Provisioner) Create(params provider.CreateSandboxParams) (provider.SandboxRuntime, error) {
	logger.Debug("create runtime requested", "id", params.ID)
	cfg := params.Config
	if cfg == nil && p.sandbox != nil {
		cfg = p.sandbox.Config
	}
	resolvedCfg, _, err := sandboxcommon.EnsureCommonConfig(params.ConfigDir, p.options.ConfigParser, cfg)
	if err != nil {
		logger.Error("runtime config dir prepare failed", "id", params.ID, "configDir", params.ConfigDir, "err", err)
		return nil, err
	}
	cfg = resolvedCfg
	params.Config = cfg
	parsed, err := p.ParseConfig(cfg)
	if err != nil {
		logger.Error("runtime config parse failed", "id", params.ID, "err", err)
		return nil, err
	}
	logger.Debug("runtime config parsed", "id", params.ID, "allowWrite", parsed.AllowWrite, "accessDirs", parsed.AccessDirs, "blockedDirs", parsed.BlockedDirs)
	syncOpts, err := p.resolveSyncOptions(params.ID, params.ConfigDir)
	if err != nil {
		logger.Error("runtime sync options resolve failed", "id", params.ID, "err", err)
		return nil, err
	}
	if err := p.SyncBundleConfig(params.ID, cfg, syncOpts); err != nil {
		logger.Error("runtime config sync failed", "id", params.ID, "err", err)
		return nil, err
	}
	if err := p.ensureRuntimeCreated(params.ID, params.ConfigDir); err != nil {
		logger.Error("runtime create failed", "id", params.ID, "err", err)
		return nil, err
	}
	sbx, err := p.state.Create(provider.ProvisionerGVisor, params, p.sandbox)
	if err != nil {
		logger.Error("create runtime failed", "id", params.ID, "err", err)
		return nil, err
	}
	createdID := ""
	createdPID := ""
	if sbx != nil && sbx.Data != nil {
		createdID = sbx.Data.ID
	}
	if sbx != nil && sbx.State != nil {
		createdPID = sbx.State.PID
	}
	logger.Info("runtime created", "id", createdID, "pid", createdPID)
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
	parsed, err := p.ParseConfig(params.Config)
	if err != nil {
		return nil, err
	}
	logger.Debug("update sandbox config parsed", "id", id, "allowWrite", parsed.AllowWrite, "accessDirs", parsed.AccessDirs, "blockedDirs", parsed.BlockedDirs)
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
	logger.Debug("list runtimes requested", "activeOnly", params.Active)
	items, err := p.state.List(params)
	if err != nil {
		logger.Error("list runtimes failed", "err", err)
		return nil, err
	}
	items = p.reconcileRuntimeState(items)
	out := make([]provider.SandboxRuntime, 0, len(items))
	for _, sbx := range items {
		out = append(out, p.wrap(sbx))
	}
	logger.Info("list runtimes completed", "count", len(out))
	return out, nil
}

func (p *Provisioner) GetSandbox(params *provider.GetSandboxParams) (provider.SandboxRuntime, error) {
	logger.Debug("get runtime requested")
	items, err := p.List(provider.ListSandboxParams{Active: false})
	if err != nil {
		return nil, err
	}
	for _, rt := range items {
		sbx := rt.Sandbox()
		if sbx == nil {
			continue
		}
		if params != nil {
			if params.Active && (sbx.State == nil || !sbx.State.Active) {
				continue
			}
			if params.PID != nil && (sbx.State == nil || sbx.State.PID != *params.PID) {
				continue
			}
			if params.Name != nil && (sbx.Data == nil || sbx.Data.ID != *params.Name) {
				continue
			}
		}
		loadedID := ""
		loadedPID := ""
		if sbx.Data != nil {
			loadedID = sbx.Data.ID
		}
		if sbx.State != nil {
			loadedPID = sbx.State.PID
		}
		logger.Info("runtime loaded", "id", loadedID, "pid", loadedPID)
		return p.wrap(sbx), nil
	}
	logger.Error("get runtime failed", "err", provider.NoProcessFound)
	return nil, provider.NoProcessFound
}

func (p *Provisioner) wrap(sbx *provider.Sandbox) *Runtime {
	return &Runtime{sandbox: provider.CloneSandbox(sbx), provisioner: p}
}

func (p *Provisioner) ExecCommand(sbx *provider.Sandbox, command string, args []string) (string, []string, error) {
	if sbx == nil || sbx.State == nil || strings.TrimSpace(sbx.State.PID) == "" {
		logger.Error("build command failed: pid missing")
		return "", nil, fmt.Errorf("sandbox: pid is required")
	}
	if strings.TrimSpace(command) == "" {
		logger.Error("build command failed: command missing", "pid", sbx.State.PID)
		return "", nil, fmt.Errorf("sandbox: command is required")
	}
	executable := p.options.Executable
	if executable == "" {
		executable = "runsc"
	}
	cmdArgs := append([]string{}, p.options.GlobalArgs...)
	rootSource := "global_args"
	rootPath, hasRootPath := runscRootFromArgs(cmdArgs)
	if !containsRunscRootFlag(cmdArgs) {
		root, err := p.resolveRunscRoot()
		if err != nil {
			return "", nil, err
		}
		if strings.TrimSpace(root) != "" {
			cmdArgs = append(cmdArgs, "--root", root)
			rootPath = root
			hasRootPath = true
			rootSource = "resolved_default"
		}
	} else if !hasRootPath {
		rootSource = "global_args_unresolved"
	}
	if hasRootPath {
		info, err := os.Stat(rootPath)
		if err != nil {
			logger.Debug("runsc root availability", "root", rootPath, "source", rootSource, "available", false, "err", err)
		} else {
			logger.Debug("runsc root availability", "root", rootPath, "source", rootSource, "available", info.IsDir(), "mode", info.Mode().String())
		}
	} else {
		logger.Debug("runsc root availability", "source", rootSource, "available", false)
	}
	if shouldAddIgnoreCgroups(cmdArgs) {
		cmdArgs = append(cmdArgs, "-ignore-cgroups")
	}
	cmdArgs = append(cmdArgs, "exec", sbx.State.PID, command)
	cmdArgs = append(cmdArgs, args...)
	cmdArgs = append(cmdArgs, p.options.ExtraArgs...)
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
	executable, cmdArgs, err := p.ExecCommand(sbx, command, args)
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

func runtimeID(sbx *provider.Sandbox) string {
	if sbx == nil || sbx.Data == nil {
		return ""
	}
	return sbx.Data.ID
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

func (p *Provisioner) ensureRuntimeCreated(id, configDir string) error {
	bundle, err := p.resolveBundle(id, configDir)
	if err != nil {
		return err
	}
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("sandbox: id is required")
	}
	entries, err := p.runtimeList()
	if err == nil {
		for _, entry := range entries {
			if entry.ID == id {
				logger.Debug("runtime already present", "id", id, "status", entry.Status)
				return nil
			}
		}
	}
	if err := p.validateRootlessUIDMapSetup(); err != nil {
		return err
	}
	if err := p.runscManage("create", "--bundle", bundle, id); err != nil {
		return p.wrapRunscCreateError(id, err)
	}
	if err := p.runscManage("start", id); err != nil {
		_ = p.runscManage("delete", id)
		return fmt.Errorf("sandbox: runsc start %s: %w", id, err)
	}
	logger.Info("runtime created in runsc", "id", id, "bundle", bundle)
	return nil
}

func (p *Provisioner) wrapRunscCreateError(id string, err error) error {
	if err == nil {
		return nil
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "cannot set up cgroup for root") || strings.Contains(lower, "cgroup.subtree_control") {
		return fmt.Errorf(
			"sandbox: runsc create %s: %w: cgroup delegation is unavailable for this user; configure systemd cgroup v2 delegation or run with -ignore-cgroups (omni sandbox adds this automatically for rootless mode)",
			id,
			err,
		)
	}
	if strings.Contains(lower, "newuidmap failed") || strings.Contains(lower, "newgidmap failed") {
		return fmt.Errorf(
			"sandbox: runsc create %s: %w: rootless user namespace mapping failed; ensure newuidmap/newgidmap are installed with setuid and /etc/subuid,/etc/subgid contain an entry for the current user",
			id,
			err,
		)
	}
	return fmt.Errorf("sandbox: runsc create %s: %w", id, err)
}

func (p *Provisioner) validateRootlessUIDMapSetup() error {
	if runtime.GOOS != "linux" {
		return nil
	}
	if os.Geteuid() == 0 {
		return nil
	}

	uidMapPath, uidErr := exec.LookPath("newuidmap")
	if uidErr != nil {
		return fmt.Errorf("sandbox: rootless gvisor requires newuidmap in PATH; install uidmap/shadow-utils: %w", uidErr)
	}
	gidMapPath, gidErr := exec.LookPath("newgidmap")
	if gidErr != nil {
		return fmt.Errorf("sandbox: rootless gvisor requires newgidmap in PATH; install uidmap/shadow-utils: %w", gidErr)
	}

	if err := ensureSetuidBinary(uidMapPath, "newuidmap"); err != nil {
		return err
	}
	if err := ensureSetuidBinary(gidMapPath, "newgidmap"); err != nil {
		return err
	}

	currentUser, userErr := user.Current()
	if userErr != nil {
		return fmt.Errorf("sandbox: resolve current user for rootless gvisor mapping: %w", userErr)
	}
	candidates := userNameCandidates(currentUser.Username, currentUser.Uid)
	if err := ensureSubIDEntry("/etc/subuid", candidates); err != nil {
		return err
	}
	if err := ensureSubIDEntry("/etc/subgid", candidates); err != nil {
		return err
	}
	return nil
}

func ensureSetuidBinary(path string, displayName string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("sandbox: stat %s: %w", displayName, err)
	}
	if info.Mode()&os.ModeSetuid == 0 {
		return fmt.Errorf("sandbox: %s at %s must have setuid bit for rootless mapping", displayName, path)
	}
	if runtime.GOOS == "linux" {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return fmt.Errorf("sandbox: stat %s at %s: unexpected file stat type", displayName, path)
		}
		if stat.Uid != 0 {
			return fmt.Errorf("sandbox: %s at %s must be owned by root for rootless mapping", displayName, path)
		}
	}
	return nil
}

func userNameCandidates(username string, uid string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, 3)
	add := func(value string) {
		key := strings.TrimSpace(value)
		if key == "" {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	add(username)
	if strings.Contains(username, "\\") {
		parts := strings.Split(username, "\\")
		add(parts[len(parts)-1])
	}
	if strings.Contains(username, "/") {
		parts := strings.Split(username, "/")
		add(parts[len(parts)-1])
	}
	add(uid)
	return out
}

func ensureSubIDEntry(filePath string, candidates []string) error {
	raw, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("sandbox: read %s: %w", filePath, err)
	}
	lines := strings.Split(string(raw), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		for _, candidate := range candidates {
			prefix := strings.TrimSpace(candidate) + ":"
			if strings.HasPrefix(trimmed, prefix) {
				return nil
			}
		}
	}
	return fmt.Errorf("sandbox: %s missing entry for current user; add a subordinate id mapping (for example: <user>:100000:65536)", filePath)
}

func marshalSandboxConfig(config *provider.Config) ([]byte, error) {
	cfg := provider.CloneConfig(config)
	if cfg == nil {
		cfg = &provider.Config{}
	}
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("sandbox: marshal sandbox config artifact: %w", err)
	}
	return raw, nil
}

func writeArtifactFile(base string, relativePath string, content []byte) error {
	baseDir := filepath.Clean(base)
	fullPath := filepath.Join(baseDir, relativePath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return fmt.Errorf("sandbox: create artifact dir %s: %w", filepath.Dir(fullPath), err)
	}
	if err := os.WriteFile(fullPath, content, 0o644); err != nil {
		return fmt.Errorf("sandbox: write artifact %s: %w", fullPath, err)
	}
	return nil
}

// resolveBundle returns the OCI bundle directory for the given sandbox.
// Priority: user-managed WorkDir bundle → configDir/gen → XDG default.
func (p *Provisioner) resolveBundle(id, configDir string) (string, error) {
	if err := validateSandboxID(id); err != nil {
		return "", err
	}
	if bundle, ok := p.bundlePath(); ok {
		return bundle, nil
	}
	if strings.TrimSpace(configDir) != "" {
		return p.ensureBundleAt(filepath.Join(strings.TrimSpace(configDir), "gen"))
	}
	return p.ensureXDGBundle(id)
}

func (p *Provisioner) resolveSyncOptions(id, configDir string) (ConfigSyncOptions, error) {
	bundleDir := ""
	if _, userManaged := p.bundlePath(); !userManaged {
		resolved, err := p.resolveBundle(id, configDir)
		if err != nil {
			return ConfigSyncOptions{}, err
		}
		bundleDir = resolved
	}
	return ConfigSyncOptions{
		ConfigDir:    configDir,
		ArtifactsDir: p.options.ArtifactsDir,
		WorkDir:      p.options.WorkDir,
		BundleDir:    bundleDir,
	}, nil
}

// ensureBundleAt creates the OCI bundle layout (dir + rootfs/ + template config.json) at bundleDir.
func (p *Provisioner) ensureBundleAt(bundleDir string) (string, error) {
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		return "", fmt.Errorf("sandbox: create bundle dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(bundleDir, gvisorBundleRootFSDirName), 0o755); err != nil {
		return "", fmt.Errorf("sandbox: create bundle rootfs: %w", err)
	}
	configPath := filepath.Join(bundleDir, gvisorBundleConfigFileName)
	if _, err := os.Stat(configPath); err != nil {
		if err := os.WriteFile(configPath, templateConfigJSON, 0o644); err != nil {
			return "", fmt.Errorf("sandbox: write bundle template config: %w", err)
		}
	}
	return bundleDir, nil
}

// ensureXDGBundle provisions the default bundle under the XDG data directory.
func (p *Provisioner) ensureXDGBundle(id string) (string, error) {
	if strings.TrimSpace(id) == "" {
		return "", fmt.Errorf("sandbox: id is required")
	}
	path, err := xdg.DataFile(filepath.Join("memory", "sandboxes", "gvisor", "bundles", id, gvisorBundleKeepFileName))
	if err != nil {
		return "", fmt.Errorf("sandbox: resolve default gvisor bundle dir: %w", err)
	}
	bundleDir, err := p.ensureBundleAt(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	logger.Info("default gvisor bundle created", "id", id, "bundle", bundleDir)
	return bundleDir, nil
}

func defaultSpec() ociSpec {
	var spec ociSpec
	spec.OCIVersion = "1.0.2"
	spec.Process.Terminal = false
	spec.Process.Args = []string{"/bin/sh", "-c", "sleep infinity"}
	spec.Process.Cwd = "/"
	spec.Root.Path = "rootfs"
	spec.Root.Readonly = true
	return spec
}

func ociMountsFor(parsed *provider.ParsedSandboxConfig, workDir string) []ociMount {
	mounts := make([]ociMount, 0, 8)
	seen := map[string]struct{}{}
	addMount := func(source, destination string, readOnly bool) {
		source = strings.TrimSpace(source)
		destination = strings.TrimSpace(destination)
		if source == "" || destination == "" {
			return
		}
		source = filepath.Clean(source)
		destination = filepath.Clean(destination)
		key := source + "->" + destination
		if _, ok := seen[key]; ok {
			return
		}
		if _, err := os.Stat(source); err != nil {
			return
		}
		options := []string{"rbind", "nosuid", "nodev"}
		if readOnly {
			options = append(options, "ro")
		} else {
			options = append(options, "rw")
		}
		mounts = append(mounts, ociMount{
			Destination: destination,
			Type:        "bind",
			Source:      source,
			Options:     options,
		})
		seen[key] = struct{}{}
	}

	// Task default: make host system binaries available inside sandbox.
	addMount("/usr/bin", "/usr/bin", true)

	// Task default: mount current workspace with full read-write access.
	if strings.TrimSpace(workDir) != "" {
		workspace := filepath.Clean(workDir)
		addMount(workspace, workspace, false)
	}

	if parsed != nil {
		for _, dir := range parsed.AccessDirs {
			addMount(dir, dir, true)
		}
	}
	return mounts
}

func (p *Provisioner) bundlePath() (string, bool) {
	workDir := strings.TrimSpace(p.options.WorkDir)
	if workDir == "" {
		return "", false
	}
	bundle := filepath.Clean(workDir)
	configPath := filepath.Join(bundle, gvisorBundleConfigFileName)
	if _, err := os.Stat(configPath); err != nil {
		return "", false
	}
	return bundle, true
}

func (p *Provisioner) reconcileRuntimeState(items []*provider.Sandbox) []*provider.Sandbox {
	entries, err := p.runtimeList()
	if err != nil {
		logger.Debug("runtime list unavailable for reconciliation", "err", err)
		return items
	}
	if len(entries) == 0 {
		return items
	}
	byID := make(map[string]runtimeEntry, len(entries))
	for _, entry := range entries {
		byID[entry.ID] = entry
	}
	out := make([]*provider.Sandbox, 0, len(items)+len(entries))
	seen := map[string]struct{}{}
	for _, sbx := range items {
		if sbx == nil || sbx.Data == nil {
			continue
		}
		entry, ok := byID[sbx.Data.ID]
		if sbx.State == nil {
			sbx.State = &provider.State{PID: sbx.Data.ID}
		}
		if ok {
			sbx.State.Active = runtimeActive(entry.Status)
			seen[sbx.Data.ID] = struct{}{}
		}
		out = append(out, provider.CloneSandbox(sbx))
	}
	for _, entry := range entries {
		if _, ok := seen[entry.ID]; ok {
			continue
		}
		out = append(out, &provider.Sandbox{
			Config: provider.CloneConfig(p.sandbox.Config),
			State: &provider.State{
				PID:    entry.ID,
				Active: runtimeActive(entry.Status),
			},
			Data: &provider.Data{
				ID:          entry.ID,
				Application: string(provider.ProvisionerGVisor),
				CreatedAt:   time.Now().UTC().Format(time.RFC3339),
			},
		})
	}
	return out
}

func (p *Provisioner) runtimeList() ([]runtimeEntry, error) {
	out, err := p.runscManageOutput("list")
	if err != nil {
		return nil, err
	}
	return parseRuntimeList(out), nil
}

func parseRuntimeList(output string) []runtimeEntry {
	entries := []runtimeEntry{}
	sc := bufio.NewScanner(strings.NewReader(output))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		if strings.EqualFold(fields[0], "id") {
			continue
		}
		entries = append(entries, runtimeEntry{
			ID:     fields[0],
			PID:    fields[1],
			Status: strings.ToLower(fields[2]),
		})
	}
	return entries
}

func runtimeActive(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "running", "created", "paused":
		return true
	default:
		return false
	}
}

func (p *Provisioner) runscManageOutput(args ...string) (string, error) {
	cmd, err := p.runscManageCommand(args...)
	if err != nil {
		return "", err
	}
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			stderr := strings.TrimSpace(string(exitErr.Stderr))
			if stderr != "" {
				return "", fmt.Errorf("%w: %s", err, stderr)
			}
		}
		return "", err
	}
	return string(out), nil
}

func (p *Provisioner) runscManage(args ...string) error {
	cmd, err := p.runscManageCommand(args...)
	if err != nil {
		return err
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg != "" {
			return fmt.Errorf("%w: %s", err, msg)
		}
		return err
	}
	return nil
}

func (p *Provisioner) runscManageCommand(args ...string) (*exec.Cmd, error) {
	executable := p.options.Executable
	if strings.TrimSpace(executable) == "" {
		executable = "runsc"
	}
	resolved, err := exec.LookPath(executable)
	if err != nil {
		return nil, fmt.Errorf("sandbox: resolve %s: %w", executable, err)
	}
	cmdArgs := append([]string{}, p.options.GlobalArgs...)
	if !containsRunscRootFlag(cmdArgs) {
		root, err := p.resolveRunscRoot()
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(root) != "" {
			cmdArgs = append(cmdArgs, "--root", root)
		}
	}
	if shouldAddIgnoreCgroups(cmdArgs) {
		cmdArgs = append(cmdArgs, "-ignore-cgroups")
	}
	cmdArgs = append(cmdArgs, args...)
	return exec.Command(resolved, cmdArgs...), nil
}

func (p *Provisioner) resolveRunscRoot() (string, error) {
	if strings.TrimSpace(p.options.RuntimeRoot) != "" {
		root := filepath.Clean(p.options.RuntimeRoot)
		if err := os.MkdirAll(root, 0o755); err != nil {
			return "", fmt.Errorf("sandbox: create runsc root %s: %w", root, err)
		}
		return root, nil
	}
	path, err := xdg.DataFile(filepath.Join("memory", "sandboxes", "gvisor", "runsc-root", ".keep"))
	if err != nil {
		return "", fmt.Errorf("sandbox: resolve default runsc root: %w", err)
	}
	root := filepath.Dir(path)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("sandbox: create default runsc root %s: %w", root, err)
	}
	return root, nil
}

func shouldAddIgnoreCgroups(args []string) bool {
	if containsRunscIgnoreCgroupsFlag(args) {
		return false
	}
	return runscNeedsIgnoreCgroupsFn()
}

func runscNeedsIgnoreCgroups() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	if os.Geteuid() != 0 {
		return true
	}
	const cgroupSubtreeControl = "/sys/fs/cgroup/cgroup.subtree_control"
	f, err := os.OpenFile(cgroupSubtreeControl, os.O_WRONLY, 0)
	if err != nil {
		return true
	}
	_ = f.Close()
	return false
}

func containsRunscRootFlag(args []string) bool {
	for i := range args {
		if args[i] == "--root" || strings.HasPrefix(args[i], "--root=") {
			return true
		}
	}
	return false
}

func runscRootFromArgs(args []string) (string, bool) {
	for i := range args {
		if args[i] == "--root" {
			if i+1 < len(args) && strings.TrimSpace(args[i+1]) != "" {
				return filepath.Clean(args[i+1]), true
			}
			return "", false
		}
		if strings.HasPrefix(args[i], "--root=") {
			value := strings.TrimSpace(strings.TrimPrefix(args[i], "--root="))
			if value == "" {
				return "", false
			}
			return filepath.Clean(value), true
		}
	}
	return "", false
}

func containsRunscIgnoreCgroupsFlag(args []string) bool {
	for i := range args {
		if args[i] == "-ignore-cgroups" || args[i] == "--ignore-cgroups" || strings.HasPrefix(args[i], "-ignore-cgroups=") || strings.HasPrefix(args[i], "--ignore-cgroups=") {
			return true
		}
	}
	return false
}

/***
***/
