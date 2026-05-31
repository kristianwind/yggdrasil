// Package docker wraps the official Docker Engine SDK with the narrow set of
// operations Yggdrasil needs: lifecycle, log/console streaming, stats and
// ephemeral install containers. It targets Docker SDK v28 (api/types split into
// per-domain subpackages).
package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
)

type Client struct {
	dc *client.Client
}

func New(host string) (*Client, error) {
	opts := []client.Opt{client.WithAPIVersionNegotiation()}
	if host != "" && host != "unix:///var/run/docker.sock" {
		opts = append(opts, client.WithHost(host))
	}
	dc, err := client.NewClientWithOpts(opts...)
	if err != nil {
		return nil, fmt.Errorf("docker client: %w", err)
	}
	return &Client{dc: dc}, nil
}

// Ping verifies the Docker daemon is reachable.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.dc.Ping(ctx)
	return err
}

type CreateOptions struct {
	Name       string
	Image      string
	Env        []string
	Cmd        []string // optional explicit command; empty uses image default
	Ports      []PortMapping
	DataDir    string // host path bind-mounted to /data
	CPUPercent float64
	MemoryMB   int64
	Labels     map[string]string
	AutoRemove bool
}

type PortMapping struct {
	HostPort      int
	ContainerPort int
	Protocol      string
}

func (c *Client) PullImage(ctx context.Context, ref string, out io.Writer) error {
	rc, err := c.dc.ImagePull(ctx, ref, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	defer rc.Close()
	if out == nil {
		out = io.Discard
	}
	_, err = io.Copy(out, rc)
	return err
}

func (c *Client) Create(ctx context.Context, opts CreateOptions) (string, error) {
	portBindings := nat.PortMap{}
	exposedPorts := nat.PortSet{}
	for _, pm := range opts.Ports {
		proto := pm.Protocol
		if proto == "" {
			proto = "tcp"
		}
		p := nat.Port(fmt.Sprintf("%d/%s", pm.ContainerPort, proto))
		exposedPorts[p] = struct{}{}
		portBindings[p] = []nat.PortBinding{{HostPort: fmt.Sprintf("%d", pm.HostPort)}}
	}

	var nanoCPU int64
	if opts.CPUPercent > 0 {
		nanoCPU = int64(opts.CPUPercent * 1e7) // 100% => 1e9 nanoCPU == 1 core
	}
	var memBytes int64
	if opts.MemoryMB > 0 {
		memBytes = opts.MemoryMB * 1024 * 1024
	}

	var mounts []mount.Mount
	if opts.DataDir != "" {
		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeBind,
			Source: opts.DataDir,
			Target: "/data",
		})
	}

	restart := container.RestartPolicy{Name: container.RestartPolicyUnlessStopped}
	if opts.AutoRemove {
		restart = container.RestartPolicy{} // no restart policy for ephemeral
	}

	resp, err := c.dc.ContainerCreate(ctx, &container.Config{
		Image:        opts.Image,
		Env:          opts.Env,
		Cmd:          opts.Cmd,
		ExposedPorts: exposedPorts,
		Labels:       opts.Labels,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		OpenStdin:    true,
		Tty:          false,
		WorkingDir:   "/data",
	}, &container.HostConfig{
		PortBindings:  portBindings,
		Mounts:        mounts,
		AutoRemove:    opts.AutoRemove,
		RestartPolicy: restart,
		Resources: container.Resources{
			NanoCPUs: nanoCPU,
			Memory:   memBytes,
		},
	}, nil, nil, opts.Name)
	if err != nil {
		return "", fmt.Errorf("create container: %w", err)
	}
	return resp.ID, nil
}

func (c *Client) Start(ctx context.Context, id string) error {
	return c.dc.ContainerStart(ctx, id, container.StartOptions{})
}

// Stop sends SIGTERM and waits up to timeoutSec before SIGKILL.
func (c *Client) Stop(ctx context.Context, id string, timeoutSec int) error {
	if timeoutSec <= 0 {
		timeoutSec = 30
	}
	return c.dc.ContainerStop(ctx, id, container.StopOptions{Timeout: &timeoutSec})
}

func (c *Client) Restart(ctx context.Context, id string) error {
	t := 30
	return c.dc.ContainerRestart(ctx, id, container.StopOptions{Timeout: &t})
}

func (c *Client) Remove(ctx context.Context, id string) error {
	return c.dc.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
}

// Logs returns a follow stream of the container's multiplexed stdout+stderr.
// Use DemuxCopy to collapse the frames into a plain byte stream.
func (c *Client) Logs(ctx context.Context, id, tail string) (io.ReadCloser, error) {
	if tail == "" {
		tail = "200"
	}
	return c.dc.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Tail:       tail,
	})
}

func (c *Client) Attach(ctx context.Context, id string) (types.HijackedResponse, error) {
	return c.dc.ContainerAttach(ctx, id, container.AttachOptions{
		Stream: true,
		Stdin:  true,
		Stdout: true,
		Stderr: true,
	})
}

type Stats struct {
	CPUPercent float64 `json:"cpu_percent"`
	MemUsageMB float64 `json:"mem_usage_mb"`
	MemLimitMB float64 `json:"mem_limit_mb"`
}

func (c *Client) GetStats(ctx context.Context, id string) (*Stats, error) {
	resp, err := c.dc.ContainerStats(ctx, id, false)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var raw container.StatsResponse
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	return &Stats{
		CPUPercent: calcCPUPercent(&raw),
		MemUsageMB: float64(raw.MemoryStats.Usage) / 1024 / 1024,
		MemLimitMB: float64(raw.MemoryStats.Limit) / 1024 / 1024,
	}, nil
}

func calcCPUPercent(s *container.StatsResponse) float64 {
	cpuDelta := float64(s.CPUStats.CPUUsage.TotalUsage) - float64(s.PreCPUStats.CPUUsage.TotalUsage)
	sysDelta := float64(s.CPUStats.SystemUsage) - float64(s.PreCPUStats.SystemUsage)
	cores := float64(len(s.CPUStats.CPUUsage.PercpuUsage))
	if cores == 0 {
		cores = float64(s.CPUStats.OnlineCPUs)
	}
	if sysDelta <= 0 || cores == 0 {
		return 0
	}
	return (cpuDelta / sysDelta) * cores * 100.0
}

func (c *Client) Inspect(ctx context.Context, id string) (container.InspectResponse, error) {
	return c.dc.ContainerInspect(ctx, id)
}

// State returns the high-level running/exited state and exit code.
func (c *Client) State(ctx context.Context, id string) (running bool, exitCode int, err error) {
	info, err := c.dc.ContainerInspect(ctx, id)
	if err != nil {
		return false, 0, err
	}
	if info.State == nil {
		return false, 0, nil
	}
	return info.State.Running, info.State.ExitCode, nil
}

// UsedHostPorts returns the set of host ports currently published by any
// container (running or not). This is the authoritative view for avoiding port
// conflicts, independent of Docker's userland-proxy mode.
func (c *Client) UsedHostPorts(ctx context.Context) (map[int]bool, error) {
	containers, err := c.dc.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, err
	}
	used := map[int]bool{}
	for _, ct := range containers {
		for _, p := range ct.Ports {
			if p.PublicPort != 0 {
				used[int(p.PublicPort)] = true
			}
		}
	}
	return used, nil
}

func (c *Client) FindByLabel(ctx context.Context, key, value string) ([]types.Container, error) {
	return c.dc.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", fmt.Sprintf("%s=%s", key, value))),
	})
}

// EphemeralOptions configures a one-shot container run.
type EphemeralOptions struct {
	Image       string
	DataDir     string            // bind-mounted to /data (optional)
	Env         []string
	Script      string            // run via /bin/sh -c
	ExtraMounts map[string]string // host path -> container path (e.g. Steam cache)
}

// RunEphemeral runs a one-shot container (e.g. a gameskill install script),
// streams its combined output to out, and blocks until it exits. A non-zero
// exit code is returned as an error. The container is always removed.
func (c *Client) RunEphemeral(ctx context.Context, img, dataDir string, env []string, script string, out io.Writer) error {
	return c.RunEphemeralOpts(ctx, EphemeralOptions{Image: img, DataDir: dataDir, Env: env, Script: script}, out)
}

// RunEphemeralOpts is the full-options form of RunEphemeral.
func (c *Client) RunEphemeralOpts(ctx context.Context, opts EphemeralOptions, out io.Writer) error {
	if out == nil {
		out = io.Discard
	}
	var mounts []mount.Mount
	if opts.DataDir != "" {
		mounts = append(mounts, mount.Mount{Type: mount.TypeBind, Source: opts.DataDir, Target: "/data"})
	}
	for host, target := range opts.ExtraMounts {
		mounts = append(mounts, mount.Mount{Type: mount.TypeBind, Source: host, Target: target})
	}

	resp, err := c.dc.ContainerCreate(ctx, &container.Config{
		Image: opts.Image,
		Env:   opts.Env,
		// Force the shell entrypoint so the script runs regardless of the image's
		// own ENTRYPOINT (e.g. steamcmd images that exec steamcmd directly).
		Entrypoint: []string{"/bin/sh", "-c"},
		Cmd:        []string{opts.Script},
		WorkingDir: "/data",
	}, &container.HostConfig{Mounts: mounts}, nil, nil, "")
	if err != nil {
		return fmt.Errorf("create ephemeral container: %w", err)
	}
	cid := resp.ID
	defer c.dc.ContainerRemove(context.Background(), cid, container.RemoveOptions{Force: true}) //nolint:errcheck

	if err := c.dc.ContainerStart(ctx, cid, container.StartOptions{}); err != nil {
		return fmt.Errorf("start ephemeral: %w", err)
	}

	logs, err := c.dc.ContainerLogs(ctx, cid, container.LogsOptions{
		ShowStdout: true, ShowStderr: true, Follow: true,
	})
	if err != nil {
		return err
	}
	defer logs.Close()
	// Block-copy the demuxed output; install scripts can run for a long time.
	_, _ = stdcopy.StdCopy(out, out, logs)

	statusCh, errCh := c.dc.ContainerWait(ctx, cid, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			return err
		}
	case status := <-statusCh:
		if status.StatusCode != 0 {
			return fmt.Errorf("install script exited with code %d", status.StatusCode)
		}
	}
	return nil
}

// DemuxCopy collapses a Docker multiplexed stream (stdout+stderr) into a single
// writer. Use it to feed log/console output to a WebSocket.
func DemuxCopy(dst io.Writer, src io.Reader) error {
	_, err := stdcopy.StdCopy(dst, dst, src)
	return err
}

// SendStdin writes a single line to a running container's stdin (its console).
// Used for games without RCON (e.g. Bedrock) to deliver scheduled commands.
func (c *Client) SendStdin(ctx context.Context, id, line string) error {
	hijack, err := c.dc.ContainerAttach(ctx, id, container.AttachOptions{Stream: true, Stdin: true})
	if err != nil {
		return err
	}
	defer hijack.Close()
	if !strings.HasSuffix(line, "\n") {
		line += "\n"
	}
	_, err = hijack.Conn.Write([]byte(line))
	return err
}
