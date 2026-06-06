package server

import (
	"time"

	serverstdio "github.com/Shaik-Sirajuddin/memory/mcp/server/stdio"
)

type StdioServer = serverstdio.StdioServer

func NewStdio(interval time.Duration) *StdioServer {
	return serverstdio.NewStdio(interval)
}

func NewStdioBridge(endpoint, socketPath string, headers map[string]string) *StdioServer {
	return serverstdio.NewStdioBridge(endpoint, socketPath, headers)
}
