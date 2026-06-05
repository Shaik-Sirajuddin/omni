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
)

var Version = "dev"

func main() {
	if printVersionOnly(os.Args[1:]) {
		return
	}

	// Set up a session-scoped log file before any loggers are constructed.
	// All in-process components and child subprocesses inherit OMNI_LOG_FILE.
	// Pre-scan args for --session-id/-sid so the log file name matches the
	// session when one is explicitly provided (e.g. re-attach flows).
	omnilog.InitSessionLog(extractSessionID(os.Args[1:]))

	var op operator.Operator
	if commandRequiresOperator(os.Args[1:]) {
		var err error
		op, err = operatorimpl.New()
		if err != nil {
			log.Fatal(err)
		}
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

// extractSessionID does a lightweight pre-scan of args for --session-id or
// -sid so InitSessionLog can name the log file after the session before full
// flag parsing runs. Returns empty string if not found.
func extractSessionID(args []string) string {
	for i, arg := range args {
		switch {
		case arg == "--session-id" || arg == "-sid":
			if i+1 < len(args) {
				return strings.TrimSpace(args[i+1])
			}
		case strings.HasPrefix(arg, "--session-id="):
			return strings.TrimSpace(strings.TrimPrefix(arg, "--session-id="))
		case strings.HasPrefix(arg, "-sid="):
			return strings.TrimSpace(strings.TrimPrefix(arg, "-sid="))
		}
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
