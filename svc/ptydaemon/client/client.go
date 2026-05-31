package client

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"sync"

	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

type request struct {
	Op        string   `json:"op"`
	SessionID string   `json:"session_id"`
	Command   []string `json:"command,omitempty"`
	Input     string   `json:"input,omitempty"`
	Key       string   `json:"key,omitempty"`
}

type response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

type Client struct {
	socketPath string
}

func New(socketPath string) *Client {
	return &Client{socketPath: socketPath}
}

func (c *Client) dial() (*net.UnixConn, error) {
	conn, err := net.Dial("unix", c.socketPath)
	if err != nil {
		return nil, err
	}
	return conn.(*net.UnixConn), nil
}

func (c *Client) roundtrip(req request) (response, *net.UnixConn, error) {
	conn, err := c.dial()
	if err != nil {
		return response{}, nil, err
	}
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		conn.Close()
		return response{}, nil, err
	}
	var resp response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		conn.Close()
		return response{}, nil, err
	}
	return resp, conn, nil
}

func (c *Client) do(req request) error {
	resp, conn, err := c.roundtrip(req)
	if conn != nil {
		conn.Close()
	}
	if err != nil {
		return err
	}
	if !resp.OK {
		return errors.New(resp.Error)
	}
	return nil
}

func (c *Client) Start(sessionID string, command []string) error {
	return c.do(request{Op: "start", SessionID: sessionID, Command: command})
}

func (c *Client) Attach(ctx context.Context, sessionID string) error {
	resp, conn, err := c.roundtrip(request{Op: "attach", SessionID: sessionID})
	if err != nil {
		return err
	}
	if !resp.OK {
		conn.Close()
		return errors.New(resp.Error)
	}

	// receive duplicate master_fd via SCM_RIGHTS
	buf := make([]byte, 32)
	oob := make([]byte, unix.CmsgSpace(4))
	_, _, _, _, err = conn.ReadMsgUnix(buf, oob)
	conn.Close()
	if err != nil {
		return err
	}

	scms, err := unix.ParseSocketControlMessage(oob)
	if err != nil || len(scms) == 0 {
		return errors.New("no control message received")
	}
	fds, err := unix.ParseUnixRights(&scms[0])
	if err != nil || len(fds) == 0 {
		return errors.New("no fd in control message")
	}

	ptmx := os.NewFile(uintptr(fds[0]), "ptmx")
	return attachToTerminal(ctx, ptmx)
}

func (c *Client) Exec(sessionID, input string) error {
	return c.do(request{Op: "exec", SessionID: sessionID, Input: input})
}

func (c *Client) Keybind(sessionID, key string) error {
	return c.do(request{Op: "keybind", SessionID: sessionID, Key: key})
}

func (c *Client) Stop(sessionID string) error {
	return c.do(request{Op: "stop", SessionID: sessionID})
}

func attachToTerminal(ctx context.Context, ptmx *os.File) error {
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return err
	}

	restore := sync.OnceFunc(func() {
		_ = term.Restore(int(os.Stdin.Fd()), oldState)
		_ = ptmx.Close()
	})

	go func() {
		defer restore()

		done := make(chan struct{}, 2)

		go func() {
			_, _ = io.Copy(ptmx, os.Stdin)
			done <- struct{}{}
		}()

		go func() {
			_, _ = io.Copy(os.Stdout, ptmx)
			done <- struct{}{}
		}()

		select {
		case <-ctx.Done():
		case <-done:
		}
	}()

	return nil
}
