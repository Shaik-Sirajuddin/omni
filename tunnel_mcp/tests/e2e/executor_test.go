//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"testing"

	"github.com/docker/docker/api/types/container"
	dockerclient "github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// CommandExecutor abstracts where commands run: local host or docker container.
type CommandExecutor interface {
	// RunCommand executes cmd, returns exit code + combined output.
	RunCommand(ctx context.Context, cmd []string) (exitCode int, out []byte, err error)
	// StreamCommand runs cmd and streams its output into w until ctx is cancelled.
	StreamCommand(ctx context.Context, w io.Writer, cmd []string) error
}

// ─── HostExecutor ──────────────────────────────────────────────────────────────

// HostExecutor runs commands directly on the current system via os/exec.
type HostExecutor struct{}

func (e *HostExecutor) RunCommand(ctx context.Context, cmd []string) (int, []byte, error) {
	c := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	out, err := c.CombinedOutput()
	exitCode := 0
	if err != nil {
		if ex, ok := err.(*exec.ExitError); ok {
			exitCode = ex.ExitCode()
		}
	}
	return exitCode, out, err
}

func (e *HostExecutor) StreamCommand(ctx context.Context, w io.Writer, cmd []string) error {
	c := exec.CommandContext(ctx, cmd[0], cmd[1:]...)
	c.Stdout = w
	c.Stderr = w
	// Start, then wait for ctx cancellation; process is killed when ctx is done.
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

// ─── DockerExecutor ────────────────────────────────────────────────────────────

// DockerExecutor proxies commands into an already-running named container
// via the Docker SDK (ContainerExecCreate + ContainerExecAttach).
// No new container is created; the named container must already be running.
type DockerExecutor struct {
	cli       *dockerclient.Client
	container string
	// env is passed to every exec so socket paths set in /etc/profile.d are available
	// without a login shell.
	env []string
	// workDir is set as the working directory for every exec call. When set to an
	// isolated test workspace, commands like `team init` (which use os.Getwd()) run
	// in the right directory rather than /build.
	workDir string
}

func newDockerExecutor(t *testing.T, containerName string) *DockerExecutor {
	t.Helper()
	cli, err := dockerclient.NewClientWithOpts(dockerclient.FromEnv, dockerclient.WithAPIVersionNegotiation())
	if err != nil {
		t.Skipf("docker executor: connect failed: %v", err)
	}
	info, err := cli.ContainerInspect(context.Background(), containerName)
	if err != nil || !info.State.Running {
		t.Skipf("docker executor: container %q not running (err=%v)", containerName, err)
	}
	// Build exec env: forward only the vars needed by the omni CLI.
	// Do NOT seed from info.Config.Env — that includes API keys from the .env.docker
	// file and would forward them to every subprocess spawned by docker exec.
	// Paths must match development/docker/entrypoint.sh.
	env := []string{
		// docker exec doesn't inherit the container's login PATH — set it explicitly
		// so agent binaries (claude, codex, gemini) are found under /usr/local/bin.
		"PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
		"OMNI_PTY_SOCKET=/run/omni-root/omni-pty.sock",
		"HOOK_OPERATOR_SOCKET=/run/omni-root/hook-operator.sock",
	}
	return &DockerExecutor{cli: cli, container: containerName, env: env}
}

// WithWorkDir returns a copy of the executor that runs all commands in dir.
func (e *DockerExecutor) WithWorkDir(dir string) *DockerExecutor {
	cp := *e
	cp.workDir = dir
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
