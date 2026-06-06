AXO_LINK_MCP_ADDR ?= :18062
AXO_LINK_SERVICE_ADDR ?= :18061
AXO_LINK_SERVICE_HTTP_BIND ?= unix
AXO_LINK_SERVICE_UNIX_SOCKET ?= /run/omni-$(USER)/service.sock
AXO_LINK_MCP_HTTP_PATH ?= /mcp
INTERVAL ?= 1m
STDIO_INTERVAL ?= 10s
PID_FILE ?= .mcp.pid
LOG_FILE ?= .mcp.log
GOCACHE ?= /tmp/go-build-mcp
BIN ?= .bin/mcp
AXO_LINK_MCP_TRANSPORT ?= streamable_http

.PHONY: setup build run run-service run-stdio start stop restart status test

setup:
	mkdir -p .bin .temp/mcp
	mkdir -p $(dir $(AXO_LINK_SERVICE_UNIX_SOCKET))

build:
	GOCACHE=$(GOCACHE) go build -o $(BIN) ./mcp/cmd/mcp

run: run-service

run-service: setup build
	AXO_LINK_MCP_TRANSPORT=streamable_http AXO_LINK_MCP_ADDR=$(AXO_LINK_MCP_ADDR) AXO_LINK_MCP_HTTP_PATH=$(AXO_LINK_MCP_HTTP_PATH) AXO_LINK_SERVICE_HTTP_BIND=$(AXO_LINK_SERVICE_HTTP_BIND) AXO_LINK_SERVICE_ADDR=$(AXO_LINK_SERVICE_ADDR) AXO_LINK_SERVICE_UNIX_SOCKET=$(AXO_LINK_SERVICE_UNIX_SOCKET) $(BIN) --transport streamable_http --addr $(AXO_LINK_MCP_ADDR) --http-path $(AXO_LINK_MCP_HTTP_PATH) --service-http-bind $(AXO_LINK_SERVICE_HTTP_BIND) --service-addr $(AXO_LINK_SERVICE_ADDR) --service-unix-socket $(AXO_LINK_SERVICE_UNIX_SOCKET) --interval $(INTERVAL)

run-stdio: build
	AXO_LINK_MCP_TRANSPORT=stdio AXO_LINK_MCP_ADDR=$(AXO_LINK_MCP_ADDR) AXO_LINK_MCP_HTTP_PATH=$(AXO_LINK_MCP_HTTP_PATH) $(BIN) --transport stdio --addr $(AXO_LINK_MCP_ADDR) --http-path $(AXO_LINK_MCP_HTTP_PATH) --interval $(STDIO_INTERVAL)

start: setup build
	AXO_LINK_MCP_TRANSPORT=streamable_http AXO_LINK_MCP_ADDR=$(AXO_LINK_MCP_ADDR) AXO_LINK_MCP_HTTP_PATH=$(AXO_LINK_MCP_HTTP_PATH) AXO_LINK_SERVICE_HTTP_BIND=$(AXO_LINK_SERVICE_HTTP_BIND) AXO_LINK_SERVICE_ADDR=$(AXO_LINK_SERVICE_ADDR) AXO_LINK_SERVICE_UNIX_SOCKET=$(AXO_LINK_SERVICE_UNIX_SOCKET) nohup $(BIN) --transport streamable_http --addr $(AXO_LINK_MCP_ADDR) --http-path $(AXO_LINK_MCP_HTTP_PATH) --service-http-bind $(AXO_LINK_SERVICE_HTTP_BIND) --service-addr $(AXO_LINK_SERVICE_ADDR) --service-unix-socket $(AXO_LINK_SERVICE_UNIX_SOCKET) --interval $(INTERVAL) >$(LOG_FILE) 2>&1 & echo $$! > $(PID_FILE)

stop:
	if test -f $(PID_FILE); then pid=$$(cat $(PID_FILE)); if test -n "$$pid" && kill -0 "$$pid" 2>/dev/null; then kill "$$pid"; fi; fi
	pkill -f "$$(pwd)/$(BIN) --transport streamable_http" 2>/dev/null || true
	rm -f $(PID_FILE)

restart: stop start

status:
	test -f $(PID_FILE) && ps -p $$(cat $(PID_FILE))

test:
	GOCACHE=$(GOCACHE) go test ./...
