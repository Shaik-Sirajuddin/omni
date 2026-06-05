//go:build e2e

package harness

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"testing"

	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// CommandExecutor abstracts where commands run: local host or docker container.
type CommandExecutor interface {
	RunCommand(ctx context.Context, cmd []string) (exitCode int, out []byte, err error)
	StreamCommand(ctx context.Context, w io.Writer, cmd []string) error
}

// ─── HostExecutor ─────────────────────────────────────────────────────────────

// HostExecutor runs commands directly on the current system via os/exec.
type HostExecutor struct {
	env     []string // extra env vars appended to os.Environ()
	workDir string   // if set, commands run with this working directory
}

// WithEnv returns a copy with additional KEY=VALUE env vars.
func (e *HostExecutor) WithEnv(vars ...string) *HostExecutor {
	cp := *e
	cp.env = append(append([]string(nil), e.env...), vars...)
	return &cp
}

// WithWorkDir returns a copy pinned to dir as the working directory.
// This is critical: omni resolves agent names against the CWD, so all
// commands for a given workspace must run from that directory.
func (e *HostExecutor) WithWorkDir(dir string) *HostExecutor {
	cp := *e
	cp.workDir = dir
	return &cp
}

func (e *HostExecutor) RunCommand(ctx context.Context, cmd []string) (int, []byte, error) {
	c := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	if len(e.env) > 0 {
		c.Env = append(os.Environ(), e.env...)
	}
	if e.workDir != "" {
		c.Dir = e.workDir
	}
	out, err := c.CombinedOutput()
	exitCode := 0
	if err != nil {
		if ex, ok := err.(*exec.ExitError); ok {
			exitCode = ex.ExitCode()
			err = nil
		}
	}
	return exitCode, out, err
}

func (e *HostExecutor) StreamCommand(ctx context.Context, w io.Writer, cmd []string) error {
	c := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	if len(e.env) > 0 {
		c.Env = append(os.Environ(), e.env...)
	}
	if e.workDir != "" {
		c.Dir = e.workDir
	}
	c.Stdout = w
	c.Stderr = w
	if err := c.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case <-ctx.Done():
		_ = c.Process.Kill()
		<-done
		return nil
	case err := <-done:
		return err
	}
}

// ─── DockerExecutor ───────────────────────────────────────────────────────────

// DockerExecutor proxies commands into an already-running named container via
// the Docker SDK. No new container is created; the named container must be running.
type DockerExecutor struct {
	cli       *dockerclient.Client
	container string
	env       []string
	workDir   string
}

func NewDockerExecutor(t *testing.T, containerName string) *DockerExecutor {
	t.Helper()
	cli, err := dockerclient.NewClientWithOpts(
		dockerclient.FromEnv,
		dockerclient.WithAPIVersionNegotiation(),
	)
	if err != nil {
		t.Skipf("docker executor: connect failed: %v", err)
	}
	info, err := cli.ContainerInspect(context.Background(), containerName)
	if err != nil || !info.State.Running {
		t.Skipf("docker executor: container %q not running (err=%v)", containerName, err)
	}
	env := []string{
		"PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"OMNI_PTY_SOCKET=/run/omni-root/omni-pty.sock",
		"HOOK_OPERATOR_SOCKET=/run/omni-root/hook-operator.sock",
	}
	return &DockerExecutor{cli: cli, container: containerName, env: env}
}

// WithWorkDir returns a copy of the executor pinned to dir for all commands.
func (e *DockerExecutor) WithWorkDir(dir string) *DockerExecutor {
	cp := *e
	cp.workDir = dir
	return &cp
}

// WithEnv returns a copy of the executor with additional KEY=VALUE env vars.
func (e *DockerExecutor) WithEnv(vars ...string) *DockerExecutor {
	cp := *e
	cp.env = append(append([]string(nil), e.env...), vars...)
	return &cp
}

func (e *DockerExecutor) RunCommand(ctx context.Context, cmd []string) (int, []byte, error) {
	resp, err := e.cli.ContainerExecCreate(ctx, e.container, container.ExecOptions{
		Cmd:          cmd,
		Env:          e.env,
		WorkingDir:   e.workDir,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return -1, nil, err
	}
	attach, err := e.cli.ContainerExecAttach(ctx, resp.ID, container.ExecStartOptions{})
	if err != nil {
		return -1, nil, err
	}
	defer attach.Close()

	var buf bytes.Buffer
	if _, err := stdcopy.StdCopy(&buf, &buf, attach.Reader); err != nil && err != io.EOF {
		return -1, buf.Bytes(), err
	}
	inspect, err := e.cli.ContainerExecInspect(ctx, resp.ID)
	if err != nil {
		return -1, buf.Bytes(), err
	}
	return inspect.ExitCode, buf.Bytes(), nil
}

func (e *DockerExecutor) StreamCommand(ctx context.Context, w io.Writer, cmd []string) error {
	resp, err := e.cli.ContainerExecCreate(ctx, e.container, container.ExecOptions{
		Cmd:          cmd,
		Env:          e.env,
		WorkingDir:   e.workDir,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return err
	}
	attach, err := e.cli.ContainerExecAttach(ctx, resp.ID, container.ExecStartOptions{})
	if err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		_, copyErr := stdcopy.StdCopy(w, w, attach.Reader)
		done <- copyErr
	}()
	select {
	case <-ctx.Done():
		attach.Close()
		<-done
		return nil
	case err := <-done:
		attach.Close()
		return err
	}
}
