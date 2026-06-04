package main

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/Shaik-Sirajuddin/memory/cli"
	"github.com/Shaik-Sirajuddin/memory/config"
	omnilog "github.com/Shaik-Sirajuddin/memory/pkg/log"
	"github.com/Shaik-Sirajuddin/memory/operator"
	operatorimpl "github.com/Shaik-Sirajuddin/memory/operator/impl"
	agentpoolclient "github.com/Shaik-Sirajuddin/memory/svc/agentpool/client"
	"github.com/Shaik-Sirajuddin/memory/pkg/sockpath"
)

var Version = "dev"

func main() {
	if printVersionOnly(os.Args[1:]) {
		return
	}

	// Set up a session-scoped log file before any loggers are constructed.
	// All in-process components and child subprocesses inherit OMNI_LOG_FILE.
	omnilog.InitSessionLog()

	var op operator.Operator
	if commandRequiresOperator(os.Args[1:]) {
		concrete, err := operatorimpl.NewDefault()
		if err != nil {
			log.Fatal(err)
		}
		// Wire the pool client so agent creates dequeue pre-warmed sessions.
		concrete.SetPoolClient(agentpoolclient.New(sockpath.AgentPool()))
		op = concrete
	}

	c := cli.EntrypointWithVersion(op, &config.DefaultOmniConfigResolver{}, Version)
	if err := c.Install(); err != nil {
		log.Fatal(err)
	}
}

func commandRequiresOperator(args []string) bool {
	command := firstCommandArg(args)
	switch command {
	case "agent", "team", "team-init":
		return true
	default:
		return false
	}
}

func firstCommandArg(args []string) string {
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" || strings.HasPrefix(arg, "-") {
			continue
		}
		return arg
	}
	return ""
}

func printVersionOnly(args []string) bool {
	if len(args) != 1 {
		return false
	}
	switch args[0] {
	case "--version", "version":
		fmt.Println(Version)
		return true
	default:
		return false
	}
}
