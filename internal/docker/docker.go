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
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
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

// Version reports the daemon's version and negotiated API version. Both empty
// when the daemon can't be reached — callers render it as "unknown" rather than
// failing, because this only ever feeds diagnostics.
func (c *Client) Version(ctx context.Context) (version, apiVersion string) {
	v, err := c.dc.ServerVersion(ctx)
	if err != nil {
		return "", ""
	}
	return v.Version, v.APIVersion
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
	// Autostart is the server's "start automatically after a reboot" setting. It
	// decides the restart policy: without it, Docker must NOT bring the container
	// back when the daemon restarts (a host reboot) — see the policy below.
	Autostart bool
	// Capabilities (cap_add), Devices ("host[:container[:perms]]"), and Sysctls let
	// special runes like Tailscale act as a subnet router / exit node.
	Capabilities []string
	Devices      []string
	Sysctls      map[string]string
	// HostMounts bind host paths into the container (e.g. a media library at
	// /mnt/mediaserver → /media). Admin-set per server, validated + read-only by
	// default — NEVER sourced from rune YAML (a rune can't mount the host fs).
	HostMounts []HostMount
	// Network attaches the container to a user-defined bridge network (instead of
	// the default bridge) so an app stack's containers can reach each other by name.
	// NetworkAlias is this container's DNS name on it. Empty = default bridge.
	Network      string
	NetworkAlias string
}

// HostMount is an admin-configured bind of a host path into a container.
type HostMount struct {
	Source   string
	Target   string
	ReadOnly bool
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
		// Same idea for address ordering: a container on the default (IPv4-only)
		// Docker bridge whose resolver returns AAAA-first can't reach vendor auth
		// services that resolve to IPv6 — Bedrock then logs "Could not connect to
		// Minecraft services" and stops, even though IPv4 egress works. If the
		// install staged a gai.conf preferring IPv4-mapped addresses, mount it at the
		// default path so getaddrinfo returns IPv4 first. Harmless for other games.
		gaiConf := filepath.Join(opts.DataDir, ".gai.conf")
		if _, err := os.Stat(gaiConf); err == nil {
			mounts = append(mounts, mount.Mount{
				Type: mount.TypeBind, Source: gaiConf, Target: "/etc/gai.conf", ReadOnly: true,
			})
		}
	}
	// Admin-configured host bind mounts (e.g. a media library at /mnt/mediaserver →
	// /media). Validated in the API layer (admin-only, denylist, source must exist);
	// read-only unless explicitly made writable. NEVER sourced from rune YAML.
	for _, hm := range opts.HostMounts {
		if hm.Source == "" || hm.Target == "" {
			continue
		}
		mounts = append(mounts, mount.Mount{
			Type: mount.TypeBind, Source: hm.Source, Target: hm.Target, ReadOnly: hm.ReadOnly,
		})
	}

	// Auto-recover from genuine crashes, but cap retries so a server that fails
	// immediately (missing jar, bad mod, bad config) stops cleanly instead of
	// crash-looping forever — the status reconciler then marks it stopped and the
	// console can show the failure logs.
	//
	// on-failure has a second, undocumented-here effect: Docker's restart-manager
	// also restarts on-failure containers when the daemon starts (a host reboot).
	// That silently overrode "Start automatically after a reboot" — a server with
	// autostart off came back anyway. So when autostart is off we use no policy:
	// Docker leaves it down on reboot, and startAutostartServers (which honours the
	// flag) doesn't touch it either. Crash recovery for those servers is the
	// opt-in watchdog's job. Autostart servers keep on-failure and come back.
	restart := restartPolicyFor(opts.Autostart)
	if opts.AutoRemove {
		restart = container.RestartPolicy{} // ephemeral install container — never restart
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
	}, netConfig(opts), nil, opts.Name)
	if err != nil {
		return "", fmt.Errorf("create container: %w", err)
	}
	return resp.ID, nil
}

// netConfig attaches the container to a user-defined network with a DNS alias, so
// an app stack's containers resolve each other by service name. nil (default
// bridge) when no network is requested.
func netConfig(opts CreateOptions) *network.NetworkingConfig {
	if opts.Network == "" {
		return nil
	}
	ep := &network.EndpointSettings{}
	if opts.NetworkAlias != "" {
		ep.Aliases = []string{opts.NetworkAlias}
	}
	return &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{opts.Network: ep},
	}
}

// EnsureNetwork creates a user-defined bridge network if it doesn't already exist.
func (c *Client) EnsureNetwork(ctx context.Context, name string) error {
	if _, err := c.dc.NetworkInspect(ctx, name, network.InspectOptions{}); err == nil {
		return nil
	}
	_, err := c.dc.NetworkCreate(ctx, name, network.CreateOptions{Driver: "bridge"})
	return err
}

// RemoveNetwork deletes a stack network (best-effort; fails harmlessly if it still
// has endpoints or is already gone).
func (c *Client) RemoveNetwork(ctx context.Context, name string) error {
	return c.dc.NetworkRemove(ctx, name)
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

// restartPolicyFor maps the "start automatically after a reboot" setting to a
// Docker restart policy. On: on-failure (recover from crashes, and come back when
// the daemon restarts). Off: none — Docker leaves it down, matching the setting.
// Create and SetRestartPolicy share this so the two never diverge.
func restartPolicyFor(autostart bool) container.RestartPolicy {
	if autostart {
		return container.RestartPolicy{Name: container.RestartPolicyOnFailure, MaximumRetryCount: 3}
	}
	return container.RestartPolicy{Name: container.RestartPolicyDisabled}
}

// SetRestartPolicy updates a running container's restart policy in place, so
// toggling autostart takes effect immediately instead of waiting for the next
// recreate (the policy is otherwise fixed at create time).
func (c *Client) SetRestartPolicy(ctx context.Context, id string, autostart bool) error {
	_, err := c.dc.ContainerUpdate(ctx, id, container.UpdateConfig{RestartPolicy: restartPolicyFor(autostart)})
	return err
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

// LogExportOptions selects a slice of a container's log for export.
//
// Tail and Since are both Docker's own filters, and they compose: "the last 200
// lines, of the last hour". Empty means unrestricted, so the zero value is the
// whole log the container still has.
type LogExportOptions struct {
	Tail       string // line count, or "all"
	Since      string // duration ("1h") or RFC3339 timestamp
	Until      string // same, for a closed range
	Timestamps bool   // prefix each line with Docker's receive time
}

// LogsExport returns a non-following reader over a slice of the container's log.
//
// The caller streams this straight to a response: a busy server's log runs to
// hundreds of MB, and none of it needs to be held in memory to be handed over.
//
// Note what "the whole log" means here: Yggdrasil recreates the container on
// every start and restart, so Docker's log for it begins at the current
// container's creation. There is no history from before the last restart to
// export, which is why the range options are relative to now rather than a
// calendar.
func (c *Client) LogsExport(ctx context.Context, id string, opt LogExportOptions) (io.ReadCloser, error) {
	tail := opt.Tail
	if tail == "" {
		tail = "all"
	}
	return c.dc.ContainerLogs(ctx, id, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     false,
		Tail:       tail,
		Since:      opt.Since,
		Until:      opt.Until,
		Timestamps: opt.Timestamps,
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

// DiskSummary is what `docker system df` prints, reduced to the numbers a panel
// can act on. All sizes in bytes.
//
// This matters more on a Yggdrasil box than it looks. Restart recreates a server
// and re-pulls its image, so every superseded version is left behind untagged and
// nothing ever removes it. Measured on the production box on 2026-09-02: 354
// images totalling 157.8 GB, of which 103.8 GB was reclaimable, while every
// server's data directory added up to about 5 GB. A disk at 93% therefore says
// almost nothing about the servers — which is exactly the wrong conclusion for a
// panel to invite, so the panel had better be able to show where the space went.
type DiskSummary struct {
	ImagesBytes           int64 `json:"images_bytes"`
	ImagesReclaimable     int64 `json:"images_reclaimable"`
	ImagesCount           int   `json:"images_count"`
	ImagesUnusedCount     int   `json:"images_unused_count"`
	ContainersBytes       int64 `json:"containers_bytes"`
	VolumesBytes          int64 `json:"volumes_bytes"`
	VolumesReclaimable    int64 `json:"volumes_reclaimable"`
	VolumesCount          int   `json:"volumes_count"`
	BuildCacheBytes       int64 `json:"build_cache_bytes"`
	BuildCacheReclaimable int64 `json:"build_cache_reclaimable"`
}

// DiskUsage asks the daemon for GET /system/df. It is not a cheap call — the
// daemon walks its own storage — so callers should cache it rather than put it
// behind a poll.
func (c *Client) DiskUsage(ctx context.Context) (*DiskSummary, error) {
	du, err := c.dc.DiskUsage(ctx, types.DiskUsageOptions{})
	if err != nil {
		return nil, err
	}
	return summarizeDiskUsage(du), nil
}

// summarizeDiskUsage is the arithmetic, split out from the call so it can be
// tested without a daemon — it is the only part here that can be quietly wrong.
func summarizeDiskUsage(du types.DiskUsage) *DiskSummary {
	out := &DiskSummary{ImagesBytes: du.LayersSize, ImagesCount: len(du.Images)}

	// Reclaimable images: the sum of the UNIQUE layers of every image no container
	// refers to. Shared layers are excluded because deleting an unreferenced image
	// does not free a layer another image still uses — so this is the space a prune
	// would actually hand back, not the nominal size of what it deletes.
	//
	// Verified against a live daemon rather than derived: `docker system df`
	// reported 6.335 GB reclaimable of 6.943 GB, and this is the only formula of
	// the five plausible ones that produced 6.335 GB. Subtracting the in-use images
	// from LayersSize — the obvious reading, and the first thing written here —
	// gave 6.915 GB, which is wrong by the size of the shared base layers.
	//
	// Containers == -1 means the daemon did not count. Such an image is not known
	// to be unused, so it is left out of the reclaimable total: over-reporting what
	// is busy is the safe direction for a number that invites deletions.
	for _, img := range du.Images {
		if img == nil {
			continue
		}
		if img.Containers != 0 {
			continue
		}
		out.ImagesUnusedCount++
		if img.Size < 0 || img.SharedSize < 0 {
			continue
		}
		out.ImagesReclaimable += img.Size - img.SharedSize
	}
	if out.ImagesReclaimable < 0 {
		out.ImagesReclaimable = 0
	}

	for _, ct := range du.Containers {
		if ct != nil {
			out.ContainersBytes += ct.SizeRw
		}
	}
	// A volume's size is -1 for any driver but "local" — skip those rather than
	// subtracting a sentinel from the total.
	for _, v := range du.Volumes {
		if v == nil || v.UsageData == nil || v.UsageData.Size < 0 {
			continue
		}
		out.VolumesCount++
		out.VolumesBytes += v.UsageData.Size
		if v.UsageData.RefCount == 0 {
			out.VolumesReclaimable += v.UsageData.Size
		}
	}
	// Build cache: the total counts every record, but only records that are neither
	// in use nor shared can be reclaimed. Also measured against the CLI — it showed
	// 20.54 GB total and 18.08 GB reclaimable, which is sum(all) and
	// sum(!InUse && !Shared) respectively. Excluding shared records from the total
	// as well, the first version here, under-reported the size by 2.5 GB.
	for _, bc := range du.BuildCache {
		if bc == nil {
			continue
		}
		out.BuildCacheBytes += bc.Size
		if !bc.InUse && !bc.Shared {
			out.BuildCacheReclaimable += bc.Size
		}
	}
	return out
}

// PruneDanglingImages removes untagged images nothing refers to and reports what
// that freed.
//
// DANGLING ONLY, and that is the whole safety argument. `docker image prune -a`
// also removes images whose containers merely happen to be stopped, which turns a
// cleanup into a forced re-pull the next time somebody presses Start — and if the
// tag has since moved or vanished upstream, that server does not come back. A
// dangling image, by contrast, is untagged and unreferenced: nothing can start
// from it, and the daemon refuses to remove one any container still uses, running
// or stopped. So the destructive version is not exposed here at all rather than
// being exposed behind a flag somebody could pass.
//
// Safe to run while a pull is in flight: an image being pulled is not dangling —
// it acquires its tag on completion, and its intermediate layers are not images.
func (c *Client) PruneDanglingImages(ctx context.Context) (deleted int, freed int64, err error) {
	rep, err := c.dc.ImagesPrune(ctx, filters.NewArgs(filters.Arg("dangling", "true")))
	if err != nil {
		return 0, 0, err
	}
	return len(rep.ImagesDeleted), int64(rep.SpaceReclaimed), nil
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

// StartedAt returns when the container's current run began (zero time if unknown),
// so callers can tell a freshly-started container from one that's been up a while.
func (c *Client) StartedAt(ctx context.Context, id string) (time.Time, error) {
	info, err := c.dc.ContainerInspect(ctx, id)
	if err != nil {
		return time.Time{}, err
	}
	if info.State == nil || info.State.StartedAt == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, info.State.StartedAt)
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

// ExitDetail reports how a stopped container ended: its exit code, and whether
// the kernel's OOM killer was what stopped it.
//
// The distinction is not cosmetic, and 137 is exactly where it bites. 137 is
// 128+9 — killed by SIGKILL — which the OOM killer produces, and so does
// Docker when a container overruns its stop timeout during an ordinary
// shutdown. Read from the code alone they are the same event, and a DayZ
// server that shut down cleanly and took a moment too long to exit got
// diagnosed here as out of memory, with more memory proposed as the fix.
// Docker knows which it was; nothing was asking it.
func (c *Client) ExitDetail(ctx context.Context, id string) (exitCode int, oomKilled bool, err error) {
	info, err := c.dc.ContainerInspect(ctx, id)
	if err != nil {
		return 0, false, err
	}
	if info.State == nil {
		return 0, false, nil
	}
	return info.State.ExitCode, info.State.OOMKilled, nil
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
	Image        string
	DataDir      string // bind-mounted to /data (optional)
	Env          []string
	Script       string            // run via /bin/sh -c
	ExtraMounts  map[string]string // host path -> container path (e.g. Steam cache)
	User         string            // optional "uid:gid"; e.g. "0:0" to force root for chown
	Network      string            // optional network to join (e.g. a stack net, to reach a db sidecar by name)
	NetworkAlias string            // DNS alias on that network

	// Argv, when non-empty, replaces the default "/bin/sh -c <Script>" with a
	// direct exec of these arguments. Two reasons to want that: an image with no
	// shell at all (the PBS client image is one static binary on busybox), and
	// arguments that must never be parsed — a Proxmox API token id contains "!",
	// and assembling a command STRING out of one is how a quoting bug ends up in
	// the middle of somebody's backup. Passing argv means nothing is parsed.
	Argv []string

	// ReadOnlyMounts are bound read-only. A backup has no business writing to
	// the directory it is reading, and telling the kernel so is stronger than
	// intending it.
	ReadOnlyMounts map[string]string

	// DataDirReadOnly applies the same to DataDir.
	DataDirReadOnly bool
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
		mounts = append(mounts, mount.Mount{
			Type: mount.TypeBind, Source: opts.DataDir, Target: "/data", ReadOnly: opts.DataDirReadOnly,
		})
	}
	for host, target := range opts.ExtraMounts {
		mounts = append(mounts, mount.Mount{Type: mount.TypeBind, Source: host, Target: target})
	}
	for host, target := range opts.ReadOnlyMounts {
		mounts = append(mounts, mount.Mount{Type: mount.TypeBind, Source: host, Target: target, ReadOnly: true})
	}

	// Default: force the shell entrypoint so the script runs regardless of the
	// image's own ENTRYPOINT (e.g. steamcmd images that exec steamcmd directly).
	entrypoint, cmd := []string{"/bin/sh", "-c"}, []string{opts.Script}
	if len(opts.Argv) > 0 {
		entrypoint, cmd = opts.Argv, nil
	}

	resp, err := c.dc.ContainerCreate(ctx, &container.Config{
		Image:      opts.Image,
		Env:        opts.Env,
		User:       opts.User, // empty = image default; "0:0" forces root (for chown)
		Entrypoint: entrypoint,
		Cmd:        cmd,
		WorkingDir: "/data",
	}, &container.HostConfig{
		Mounts:    mounts,
		Resources: container.Resources{PidsLimit: defaultPidsLimit()},
	}, netConfig(CreateOptions{Network: opts.Network, NetworkAlias: opts.NetworkAlias}), nil, "")
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
			what := "install script"
			if len(opts.Argv) > 0 {
				what = filepath.Base(opts.Argv[0])
			}
			return fmt.Errorf("%s exited with code %d", what, status.StatusCode)
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
