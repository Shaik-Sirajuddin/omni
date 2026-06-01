package gvisor

import (
	"fmt"
	"strings"

	sandboxcommon "github.com/Shaik-Sirajuddin/omni/sandbox/common"

	"github.com/Shaik-Sirajuddin/omni/sandbox/provider"
)

func (r *Runtime) Sandbox() *provider.Sandbox { return provider.CloneSandbox(r.sandbox) }

func (r *Runtime) Command(command string, args []string) error {
	logger.Debug("runtime command", "id", runtimeID(r.sandbox), "command", command, "args", args)
	return r.provisioner.run(r.sandbox, command, args, true)
}

func (r *Runtime) Execute(command string, args []string) error {
	logger.Debug("runtime execute", "id", runtimeID(r.sandbox), "command", command, "args", args)
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
		logger.Error("runtime sync failed: missing sandbox")
		return fmt.Errorf("sandbox: runtime sandbox is required")
	}
	configDir := ""
	if r.sandbox != nil && r.sandbox.Data != nil {
		configDir = r.sandbox.Data.ConfigDir
	}
	syncedConfig := provider.CloneConfig(config)
	if strings.TrimSpace(configDir) != "" {
		if r.provisioner.options.ConfigParser == nil {
			logger.Error("runtime sync failed: config parser missing", "id", runtimeID(r.sandbox), "configDir", configDir)
			return fmt.Errorf("sandbox: config parser is required for config dir sync")
		}
		updatedConfig, _, err := sandboxcommon.SyncCommonConfig(configDir, r.provisioner.options.ConfigParser, config)
		if err != nil {
			logger.Error("runtime sync common config failed", "id", runtimeID(r.sandbox), "configDir", configDir, "err", err)
			return err
		}
		syncedConfig = updatedConfig
	}
	syncOpts, err := r.provisioner.resolveSyncOptions(r.sandbox.Data.ID, configDir)
	if err != nil {
		logger.Error("runtime sync options resolve failed", "id", runtimeID(r.sandbox), "err", err)
		return err
	}
	if err := r.provisioner.SyncBundleConfig(r.sandbox.Data.ID, syncedConfig, syncOpts); err != nil {
		logger.Error("runtime sync bundle failed", "id", runtimeID(r.sandbox), "err", err)
		return err
	}
	r.sandbox.Config = provider.CloneConfig(syncedConfig)
	r.provisioner.state.SyncOne(r.sandbox.Data.ID, syncedConfig)
	logger.Info("runtime synced", "id", runtimeID(r.sandbox))
	return nil
}
