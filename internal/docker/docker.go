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
	"os"
	"path/filepath"
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
	Name      string
	Image     string
	Env       []string
	Cmd       []string // optional explicit command; empty uses image default
	User      string   // "uid:gid" — run as the panel user so files stay editable
	Ports     []PortMapping
	DataDir   string // host path bind-mounted into the container
	DataMount string // mount target for DataDir (default /data); apps may differ
	// ExtraVolumes are additional container paths that each get their own persisted
	// directory (a subdir of DataDir), for images that require more than one mount
	// (e.g. Nginx Proxy Manager needs both /data and /etc/letsencrypt).
	ExtraVolumes   []string
	KeepEntrypoint bool // run the image's own ENTRYPOINT instead of clearing it
	CPUPercent     float64
	MemoryMB       int64
	Labels         map[string]string
	AutoRemove     bool
	// Capabilities (cap_add), Devices ("host[:container[:perms]]"), and Sysctls let
	// special runes like Tailscale act as a subnet router / exit node.
	Capabilities []string
	Devices      []string
	Sysctls      map[string]string
}

// defaultPidsLimit caps the number of processes a container may spawn, so a
// runaway/forked process (fork bomb) in one server can't exhaust the host's PID
// table and take down the panel + other servers. Generous enough for any real
// workload (game servers, SteamCMD, app stacks).
func defaultPidsLimit() *int64 {
	n := int64(8192)
	return &n
}

// parseDeviceMappings converts "host[:container[:perms]]" strings to Docker device
// mappings (container path defaults to the host path; perms default to "rwm").
func parseDeviceMappings(devs []string) []container.DeviceMapping {
	var out []container.DeviceMapping
	for _, d := range devs {
		d = strings.TrimSpace(d)
		if d == "" {
			continue
		}
		parts := strings.SplitN(d, ":", 3)
		m := container.DeviceMapping{PathOnHost: parts[0], PathInContainer: parts[0], CgroupPermissions: "rwm"}
		if len(parts) >= 2 && parts[1] != "" {
			m.PathInContainer = parts[1]
		}
		if len(parts) == 3 && parts[2] != "" {
			m.CgroupPermissions = parts[2]
		}
		out = append(out, m)
	}
	return out
}

// extraVolumeSubdir maps a container path to a filesystem-safe subdir name under
// the server's data dir (e.g. "/etc/letsencrypt" -> "_etc_letsencrypt").
func extraVolumeSubdir(containerPath string) string {
	s := strings.Trim(containerPath, "/")
	r := strings.NewReplacer("/", "_", ".", "_", " ", "_")
	return "_" + r.Replace(s)
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

	dataMount := opts.DataMount
	// WorkingDir defaults to /data for games (so `./Binary` startup commands work);
	// for app runes with a custom data_path we leave it to the image's own WORKDIR.
	workDir := "/data"
	if dataMount == "" {
		dataMount = "/data"
	} else {
		workDir = ""
	}
	var mounts []mount.Mount
	if opts.DataDir != "" {
		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeBind,
			Source: opts.DataDir,
			Target: dataMount,
		})
		// Additional persisted volumes (a subdir of the data dir each) for images
		// that require more than one mount (e.g. NPM's /data + /etc/letsencrypt).
		for _, vp := range opts.ExtraVolumes {
			if vp == "" {
				continue
			}
			src := filepath.Join(opts.DataDir, extraVolumeSubdir(vp))
			os.MkdirAll(src, 0o775) //nolint:errcheck // bind source must exist
			mounts = append(mounts, mount.Mount{Type: mount.TypeBind, Source: src, Target: vp})
		}
		// When running as an explicit uid that may not exist in the image's
		// /etc/passwd, provide a minimal passwd so getpwuid(uid) succeeds. Steam
		// servers (DayZ, Rust) call getpwuid and segfault on a NULL result — which
		// surfaced as DayZ dying with "CrashReporter: not found". Harmless for
		// other images (Java etc. don't consult it).
		//
		// BUT only for our own-command runes. KeepEntrypoint app images run their own
		// init and rely on their image's named users (gitea's "git",
		// nextcloud/wordpress's "www-data"); clobbering /etc/passwd with our minimal
		// one deletes those users and the entrypoint dies ("unknown user git",
		// "apache2: bad user name www-data"). Those images already ship the passwd they
		// need, so they never required this shim.
		if opts.User != "" && !opts.KeepEntrypoint {
			if pw, err := writePasswdFile(opts.DataDir, opts.User); err == nil {
				mounts = append(mounts, mount.Mount{
					Type: mount.TypeBind, Source: pw, Target: "/etc/passwd", ReadOnly: true,
				})
			}
		}
		// Some game binaries (Minecraft Bedrock's libcurl) only trust the system CA
		// bundle at its compiled-in default path and ignore SSL_CERT_FILE/CURL_CA_BUNDLE,
		// so online-mode TLS to the vendor's auth services fails on a bare base image.
		// If the install staged a CA bundle in the data dir, mount it at the default
		// path. Harmless for games that don't need it.
		caBundle := filepath.Join(opts.DataDir, ".certs", "ca-certificates.crt")
		if _, err := os.Stat(caBundle); err == nil {
			mounts = append(mounts, mount.Mount{
				Type: mount.TypeBind, Source: caBundle, Target: "/etc/ssl/certs/ca-certificates.crt", ReadOnly: true,
			})
		}
	}

	// Auto-recover from genuine crashes, but cap retries so a server that fails
	// immediately (missing jar, bad mod, bad config) stops cleanly instead of
	// crash-looping forever — the status reconciler then marks it stopped and the
	// console can show the failure logs.
	restart := container.RestartPolicy{Name: container.RestartPolicyOnFailure, MaximumRetryCount: 3}
	if opts.AutoRemove {
		restart = container.RestartPolicy{} // no restart policy for ephemeral
	}

	// Clear any image ENTRYPOINT so our Cmd is the actual command — otherwise images
	// like cm2network/steamcmd would pass our startup command as args to their own
	// entrypoint (manifesting as "./RustDedicated: not found"). App runes that need
	// the image's own entrypoint (e.g. linuxserver.io s6) set KeepEntrypoint.
	entrypoint := []string{}
	if opts.KeepEntrypoint {
		entrypoint = nil // nil = use the image's ENTRYPOINT
	}
	resp, err := c.dc.ContainerCreate(ctx, &container.Config{
		Image:        opts.Image,
		Env:          opts.Env,
		User:         opts.User,
		Entrypoint:   entrypoint,
		Cmd:          opts.Cmd, // empty with KeepEntrypoint = image default CMD
		ExposedPorts: exposedPorts,
		Labels:       opts.Labels,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		OpenStdin:    true,
		Tty:          false,
		WorkingDir:   workDir,
	}, &container.HostConfig{
		PortBindings:  portBindings,
		Mounts:        mounts,
		AutoRemove:    opts.AutoRemove,
		RestartPolicy: restart,
		CapAdd:        opts.Capabilities,
		Sysctls:       opts.Sysctls,
		Resources: container.Resources{
			NanoCPUs:  nanoCPU,
			Memory:    memBytes,
			Devices:   parseDeviceMappings(opts.Devices),
			PidsLimit: defaultPidsLimit(), // cap process count to blunt fork bombs
		},
	}, nil, nil, opts.Name)
	if err != nil {
		return "", fmt.Errorf("create container: %w", err)
	}
	return resp.ID, nil
}

// writePasswdFile writes a minimal /etc/passwd (root + the run-as user + nobody)
// next to the servers directory and returns its path, for bind-mounting into a
// runtime container. user is "uid:gid". The file only depends on the panel uid,
// so it's shared across servers.
func writePasswdFile(dataDir, user string) (string, error) {
	uid, gid := user, user
	if parts := strings.SplitN(user, ":", 2); len(parts) == 2 {
		uid, gid = parts[0], parts[1]
	}
	content := "root:x:0:0:root:/root:/bin/sh\n" +
		fmt.Sprintf("ygg:x:%s:%s:ygg:/data:/bin/sh\n", uid, gid) +
		"nobody:x:65534:65534:nobody:/nonexistent:/usr/sbin/nologin\n"
	path := filepath.Join(filepath.Dir(dataDir), ".ygg-passwd")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		return "", err
	}
	return path, nil
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

// LogsSnapshot returns the current container logs without following, so the
// reader reaches EOF — for one-shot reads like startup-readiness checks.
func (c *Client) LogsSnapshot(ctx context.Context, id, tail string) (io.ReadCloser, error) {
	if tail == "" {
		tail = "200"
	}
	return c.dc.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     false,
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
	if sysDelta <= 0 || cpuDelta < 0 {
		return 0
	}
	// Report CPU as a share of the WHOLE host (0–100%), matching the dashboard's
	// host-CPU card. Docker's usual per-core formula multiplies by the core count,
	// so a container using >1 core reads >100% (e.g. 120% for 1.2 of 8 cores),
	// which looks wrong on a per-server gauge. sysDelta already spans all cores,
	// so cpuDelta/sysDelta is the fraction of total capacity.
	pct := (cpuDelta / sysDelta) * 100.0
	if pct > 100 {
		pct = 100
	}
	return pct
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
	DataDir     string // bind-mounted to /data (optional)
	Env         []string
	Script      string            // run via /bin/sh -c
	ExtraMounts map[string]string // host path -> container path (e.g. Steam cache)
	User        string            // optional "uid:gid"; e.g. "0:0" to force root for chown
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
		User:  opts.User, // empty = image default; "0:0" forces root (for chown)
		// Force the shell entrypoint so the script runs regardless of the image's
		// own ENTRYPOINT (e.g. steamcmd images that exec steamcmd directly).
		Entrypoint: []string{"/bin/sh", "-c"},
		Cmd:        []string{opts.Script},
		WorkingDir: "/data",
	}, &container.HostConfig{
		Mounts:    mounts,
		Resources: container.Resources{PidsLimit: defaultPidsLimit()},
	}, nil, nil, "")
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
