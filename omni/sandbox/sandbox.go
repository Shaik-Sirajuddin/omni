package sandbox

import (
	"errors"
	"fmt"
	"runtime"

	"github.com/Shaik-Sirajuddin/omni/sandbox/provider"
	"github.com/Shaik-Sirajuddin/omni/sandbox/provider/bubblewrap"
	"github.com/Shaik-Sirajuddin/omni/sandbox/provider/gvisor"
	"github.com/Shaik-Sirajuddin/omni/sandbox/provider/seatbelt"
	sandboxstore "github.com/Shaik-Sirajuddin/omni/sandbox/store"
)

type WorkspaceDir = provider.WorkspaceDir
type MountConfig = provider.MountConfig
type Policy = provider.Policy
type Config = provider.Config
type State = provider.State
type Data = provider.Data
type Sandbox = provider.Sandbox
type FSPolicy = provider.FSPolicy
type AgentFSPolicy = provider.AgentFSPolicy
type ProvisionerKind = provider.ProvisionerKind
type ProvisionerOptions = provider.ProvisionerOptions
type GetSandboxParams = provider.GetSandboxParams
type ListSandboxParams = provider.ListSandboxParams
type CreateSandboxParams = provider.CreateSandboxParams
type UpdateSandboxParams = provider.UpdateSandboxParams
type ParsedSandboxConfig = provider.ParsedSandboxConfig
type SandboxRuntime = provider.SandboxRuntime
type SandboxProvisioner = provider.SandboxProvisioner
type SandboxDirProvisioner = provider.SandboxDirProvisioner
type SandboxUpdateProvisioner = provider.SandboxUpdateProvisioner
type SandboxConfigParser = provider.SandboxConfigParser
type Info = provider.Info
type Store = provider.Store

type ConfigParser interface {
	Load(filePath string) (*Config, error)
	Validate(config *Config) error
	Save(config *Config, filePath string) error
}

const (
	Default               = provider.Default
	AllPermissiveRead     = provider.AllPermissiveRead
	PermissiveRead        = provider.PermissiveRead
	NonDependent          = provider.NonDependent
	Inherit               = provider.Inherit
	DefaultPolicy         = provider.DefaultPolicy
	ProvisionerGVisor     = provider.ProvisionerGVisor
	ProvisionerBubblewrap = provider.ProvisionerBubblewrap
	ProvisionerSeatbelt   = provider.ProvisionerSeatbelt
)

var (
	NoProcessFound              = errors.New("no process found")
	configParser   ConfigParser = defaultConfigParser{}
)

func NewProvisioner(kind ProvisionerKind, sbx *Sandbox, opts ProvisionerOptions) (SandboxProvisioner, error) {
	if opts.Store == nil {
		store, err := sandboxstore.GetSandboxStore(string(kind))
		if err != nil {
			return nil, err
		}
		opts.Store = store
	}
	if opts.ConfigParser == nil {
		opts.ConfigParser = configParser
	}
	switch kind {
	case ProvisionerGVisor:
		return gvisor.New(sbx, opts)
	case ProvisionerBubblewrap:
		return bubblewrap.New(sbx, opts)
	case ProvisionerSeatbelt:
		return seatbelt.New(sbx, opts)
	default:
		return nil, fmt.Errorf("sandbox: unsupported provisioner %q", kind)
	}
}

func SupportedProvisioners(goos string) []ProvisionerKind {
	switch goos {
	case "linux":
		return []ProvisionerKind{ProvisionerGVisor}
	case "darwin":
		return []ProvisionerKind{ProvisionerSeatbelt}
	default:
		return nil
	}
}

func HostSupportedProvisioners() []ProvisionerKind {
	return SupportedProvisioners(runtime.GOOS)
}

func NewConfigParser() ConfigParser {
	return configParser
}

func Load(filePath string) (*Config, error) {
	return configParser.Load(filePath)
}

func Validate(config *Config) error {
	return configParser.Validate(config)
}

func Save(config *Config, filePath string) error {
	return configParser.Save(config, filePath)
}
