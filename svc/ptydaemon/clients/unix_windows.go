//go:build windows

package clients

import (
	"context"
	"errors"
)

var errWindows = errors.New("ptydaemon: not supported on Windows")

func newUnixClient() Client { return &unsupportedClient{} }

type unsupportedClient struct{}

func (c *unsupportedClient) Pipe(_, _ string, _ []byte) error                              { return errWindows }
func (c *unsupportedClient) Start(_ string, _ []string, _ []string, _, _ string) error     { return errWindows }
func (c *unsupportedClient) Attach(_ context.Context, _ string) error                      { return errWindows }
func (c *unsupportedClient) Exec(_, _ string) error                                        { return errWindows }
func (c *unsupportedClient) Stop(_ string) error                                           { return errWindows }
func (c *unsupportedClient) StopSafe(_ string, _ bool) error                               { return errWindows }
func (c *unsupportedClient) Detach(_ string) error                                         { return errWindows }
func (c *unsupportedClient) Register(_, _, _ string) error                                 { return errWindows }
func (c *unsupportedClient) List(_ string) ([]*PTYTerminalInfo, error)                     { return nil, errWindows }
func (c *unsupportedClient) Get(_, _ string) (*PTYTerminalInfo, error)                     { return nil, errWindows }
func (c *unsupportedClient) ListAttached(_ string) ([]AttachedProcess, error)              { return nil, errWindows }
func (c *unsupportedClient) MetaAttached(_ string) (int, error)                            { return 0, errWindows }
