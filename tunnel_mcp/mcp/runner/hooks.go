package runner

import (
	"fmt"
	"net"
	"strings"

	"github.com/Shaik-Sirajuddin/memory/config"
	"github.com/Shaik-Sirajuddin/memory/connector/codeagent/hooks"
)

type hookResolver interface {
	AddHooks(map[string][]config.HookEntry) (int, error)
}

func provisionDefaultHooks(cfg Config) error {
	return provisionDefaultHooksWithResolver(cfg, &config.DefaultOmniConfigResolver{})
}

func provisionDefaultHooksWithResolver(cfg Config, resolver hookResolver) error {
	if resolver == nil {
		return fmt.Errorf("omni config resolver is unavailable")
	}
	hookURL := defaultHookURL(cfg)
	entry := config.HookEntry{Url: &hookURL}
	added, err := resolver.AddHooks(map[string][]config.HookEntry{
		string(hooks.SessionStart):    {entry},
		string(hooks.PrePrompt):          {entry},
		string(hooks.PostPrompt):         {entry},
		string(hooks.PostToolUseFailure): {entry},
	})
	if err != nil {
		return fmt.Errorf("provision omni hooks: %w", err)
	}
	logger.Info("omni hooks provisioned", "added", added, "url", hookURL)
	return nil
}

func defaultHookURL(cfg Config) string {
	if cfg.ServiceHTTPBind == ServiceHTTPBindTCP {
		return tcpURL(cfg.ServiceAddr, "/hook")
	}
	return "unix://" + cfg.ServiceUnixSocket + "/hook"
}

func tcpHookURL(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err == nil {
		switch host {
		case "", "0.0.0.0", "::", "[::]":
			host = "127.0.0.1"
		}
		return "http://" + net.JoinHostPort(host, port) + "/hook"
	}
	if strings.HasPrefix(addr, ":") {
		return "http://127.0.0.1" + addr + "/hook"
	}
	return "http://" + strings.TrimRight(addr, "/") + "/hook"
}
