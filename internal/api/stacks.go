package api

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/kristianwind/yggdrasil/internal/docker"
	"github.com/kristianwind/yggdrasil/internal/gameskill"
)

// App-stack support. A rune with a services: block is a small multi-container app
// (e.g. Immich = server + machine-learning + postgres + redis, Paperless = app +
// redis). The panel gives it a private bridge network, runs each sidecar on it with
// its service name as a DNS alias, and joins the main container to the same network
// so the app reaches its database and cache by name. Sidecars publish no host ports,
// keep their image's own entrypoint, and each persist to a subdir of the server's
// data dir. Everything is gated on len(gs.Services) > 0, so ordinary single-container
// servers take exactly the same path they always did.

func stackNetworkName(id string) string   { return "ygg-net-" + id[:8] }
func sidecarName(id, svc string) string    { return fmt.Sprintf("ygg-%s-%s", id[:8], svc) }
func stackDataDir(dataDir, svc string) string { return filepath.Join(dataDir, ".stack", svc) }

// startStack ensures the per-server network exists and (re)creates every sidecar on
// it. Called before the main container is created so its dependencies are up. The
// main container itself is attached to the network by the caller.
func (s *Server) startStack(ctx context.Context, id, dataDir string, gs *gameskill.Gameskill, env map[string]string) error {
	if len(gs.Services) == 0 {
		return nil
	}
	netName := stackNetworkName(id)
	if err := s.docker.EnsureNetwork(ctx, netName); err != nil {
		return fmt.Errorf("stack network: %w", err)
	}
	for _, svc := range gs.Services {
		name := sidecarName(id, svc.Name)
		s.docker.Remove(ctx, name) // clear any orphan with our deterministic name

		// A sidecar's data dir must be writable by the image's own user (postgres,
		// redis run as their own uid), so it can't inherit the panel user's perms.
		var srcDir string
		if svc.DataPath != "" {
			srcDir = stackDataDir(dataDir, svc.Name)
			if err := os.MkdirAll(srcDir, 0o777); err != nil {
				return fmt.Errorf("sidecar %s data dir: %w", svc.Name, err)
			}
			os.Chmod(srcDir, 0o777) //nolint:errcheck // best-effort widen for the container user
		}

		envSlice := make([]string, 0, len(svc.Env))
		for k, v := range svc.Env {
			envSlice = append(envSlice, fmt.Sprintf("%s=%s", k, gameskill.ApplyTemplate(v, env)))
		}
		var cmd []string
		for _, a := range svc.Command {
			cmd = append(cmd, gameskill.ApplyTemplate(a, env))
		}
		image := gameskill.ApplyTemplate(svc.Image, env)
		s.docker.PullImage(ctx, image, io.Discard)

		cid, err := s.docker.Create(ctx, docker.CreateOptions{
			Name:           name,
			Image:          image,
			Env:            envSlice,
			Cmd:            cmd,
			DataDir:        srcDir,       // "" = no volume (e.g. a stateless worker)
			DataMount:      svc.DataPath, // the image's own data path
			KeepEntrypoint: true,         // sidecars run their image entrypoint (initdb etc.)
			Autostart:      true,         // a stack sidecar returns with its app after a reboot
			Network:        netName,
			NetworkAlias:   svc.Name,
			Labels:         map[string]string{"yggdrasil.server_id": id, "yggdrasil.role": "sidecar"},
		})
		if err != nil {
			return fmt.Errorf("create sidecar %s: %w", svc.Name, err)
		}
		if err := s.docker.Start(ctx, cid); err != nil {
			return fmt.Errorf("start sidecar %s: %w", svc.Name, err)
		}
	}
	return nil
}

// stopStack removes a server's sidecar containers (data persists in their volumes).
func (s *Server) stopStack(ctx context.Context, id string, gs *gameskill.Gameskill) {
	for _, svc := range gs.Services {
		s.docker.Remove(ctx, sidecarName(id, svc.Name))
	}
}

// removeStack tears the stack down entirely: sidecars then the network. Called when
// a server is deleted.
func (s *Server) removeStack(ctx context.Context, id string, gs *gameskill.Gameskill) {
	s.stopStack(ctx, id, gs)
	if len(gs.Services) > 0 {
		s.docker.RemoveNetwork(ctx, stackNetworkName(id))
	}
}
