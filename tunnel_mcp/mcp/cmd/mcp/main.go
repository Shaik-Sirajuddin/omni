package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Shaik-Sirajuddin/omni/mcp/mcp/runner"
	pkglog "github.com/Shaik-Sirajuddin/omni/pkg/log"
)

var logger = pkglog.NewLogger("component", "mcp-main")

func main() {
	defaults := runner.DefaultConfig()
	addr := flag.String("addr", defaults.Addr, "MCP streamable HTTP listen address, or stdio target base URL")
	interval := flag.Duration("interval", defaults.Interval, "interval for automatic hello sampling requests")
	transport := flag.String("transport", defaults.Transport, "MCP transport: streamable_http or stdio")
	serviceHTTPBind := flag.String("service-http-bind", defaults.ServiceHTTPBind, "pure HTTP service bind: unix, tcp, or disabled")
	serviceAddr := flag.String("service-addr", defaults.ServiceAddr, "pure HTTP service TCP listen address")
	serviceSocketPath := flag.String("service-unix-socket", defaults.ServiceUnixSocket, "pure HTTP service unix socket path")
	httpPath := flag.String("http-path", defaults.HTTPPath, "MCP streamable HTTP path")
	flag.Parse()
	intervalSet := flagWasSet("interval")

	if *transport == runner.TransportStdio {
		if !intervalSet {
			*interval = runner.DefaultStdioInterval
		}
	}
	if *transport != runner.TransportStreamableHTTP && *transport != runner.TransportStdio {
		_, _ = fmt.Fprintf(os.Stderr, "unsupported MCP transport %q\n", *transport)
		os.Exit(2)
	}
	if *serviceHTTPBind != runner.ServiceHTTPBindUnix && *serviceHTTPBind != runner.ServiceHTTPBindTCP && *serviceHTTPBind != runner.ServiceHTTPBindDisabled {
		_, _ = fmt.Fprintf(os.Stderr, "unsupported service HTTP bind %q\n", *serviceHTTPBind)
		os.Exit(2)
	}

	ctx, stopSignals := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stopSignals()

	cfg := runner.Config{
		Transport:         *transport,
		Addr:              *addr,
		Interval:          *interval,
		ServiceHTTPBind:   *serviceHTTPBind,
		ServiceAddr:       *serviceAddr,
		ServiceUnixSocket: *serviceSocketPath,
		HTTPPath:          *httpPath,
		Stdin:             os.Stdin,
		Stdout:            os.Stdout,
	}
	if err := runner.Run(ctx, cfg); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("mcp failed", "err", err)
		os.Exit(1)
	}
}

func flagWasSet(name string) bool {
	wasSet := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			wasSet = true
		}
	})
	return wasSet
}
