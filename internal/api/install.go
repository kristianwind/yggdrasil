package api

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/kristianwind/yggdrasil/internal/docker"
	"github.com/kristianwind/yggdrasil/internal/gameskill"
)

// progressHub is a tiny in-memory pub/sub for streaming long-running build/
// install output to WebSocket subscribers. It keeps a bounded history so a
// client that connects mid-install still sees prior lines.
type progressHub struct {
	mu      sync.Mutex
	subs    map[string]map[chan string]struct{}
	history map[string][]string
	active  map[string]bool
}

const historyCap = 500

func newProgressHub() *progressHub {
	return &progressHub{
		subs:    map[string]map[chan string]struct{}{},
		history: map[string][]string{},
		active:  map[string]bool{},
	}
}

func (h *progressHub) publish(id, line string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	hist := append(h.history[id], line)
	if len(hist) > historyCap {
		hist = hist[len(hist)-historyCap:]
	}
	h.history[id] = hist
	for ch := range h.subs[id] {
		select {
		case ch <- line:
		default: // drop for slow consumers; history backfills on reconnect
		}
	}
}

func (h *progressHub) subscribe(id string) (chan string, []string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := make(chan string, 256)
	if h.subs[id] == nil {
		h.subs[id] = map[chan string]struct{}{}
	}
	h.subs[id][ch] = struct{}{}
	hist := append([]string(nil), h.history[id]...)
	return ch, hist
}

func (h *progressHub) unsubscribe(id string, ch chan string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if subs := h.subs[id]; subs != nil {
		delete(subs, ch)
	}
	close(ch)
}

func (h *progressHub) setActive(id string, v bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.active[id] = v
}

func (h *progressHub) isActive(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.active[id]
}

// hubWriter adapts an io.Writer to progressHub.publish, splitting on newlines.
type hubWriter struct {
	hub *progressHub
	id  string
}

func (w hubWriter) Write(p []byte) (int, error) {
	sc := bufio.NewScanner(strings.NewReader(string(p)))
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		w.hub.publish(w.id, sc.Text())
	}
	return len(p), nil
}

// runInstall executes a server's gameskill install script in an ephemeral
// container, streaming output to the hub and recording the final status. It is
// safe to call in a goroutine; it serializes per server via the active flag.
func (s *Server) runInstall(serverID string) error {
	if s.install.isActive(serverID) {
		return fmt.Errorf("install already running")
	}
	s.install.setActive(serverID, true)
	defer s.install.setActive(serverID, false)

	ctx := context.Background()
	rt, err := s.loadRuntime(ctx, serverID)
	if err != nil {
		return err
	}
	var dataDir string
	if err := s.db.QueryRowContext(ctx, "SELECT data_dir FROM servers WHERE id=?", serverID).Scan(&dataDir); err != nil {
		return err
	}

	if rt.gs.Install == nil {
		// Nothing to install; mark ready immediately.
		s.db.ExecContext(ctx, "UPDATE servers SET installed=1, install_status='done' WHERE id=?", serverID)
		return nil
	}

	w := hubWriter{hub: s.install, id: serverID}
	s.db.ExecContext(ctx, "UPDATE servers SET install_status='installing' WHERE id=?", serverID)
	s.install.publish(serverID, fmt.Sprintf("=== Install started %s ===", time.Now().UTC().Format(time.RFC3339)))

	env := installEnv(rt)
	image := gameskill.ApplyTemplate(rt.gs.Install.Image, rt.env)
	if image == "" {
		image = gameskill.ApplyTemplate(rt.gs.Docker.Image, rt.env)
	}
	script := gameskill.ApplyTemplate(rt.gs.Install.Script, rt.env)

	// Steam games: anonymous ones just run; account-required ones (DayZ) reuse the
	// host's authorized account + persistent SteamCMD cache so Steam Guard isn't
	// re-triggered. The cache is mounted into the install container too.
	extraMounts := map[string]string{}
	if rt.gs.Steam != nil {
		extraMounts[s.steamCacheDir()] = "/steamcache"
		env["HOME"] = "/steamcache"
		// The SteamCMD image runs as a non-root user; make the bind-mounted data
		// dir writable so it can install the game into /data.
		os.Chmod(dataDir, 0777) //nolint:errcheck
		if !rt.gs.Steam.Anonymous {
			user := s.authorizedSteamUser(ctx)
			if user == "" {
				s.db.ExecContext(ctx, "UPDATE servers SET install_status='error' WHERE id=?", serverID)
				msg := "This game requires an authorized Steam account. Authorize one under Settings → Steam first."
				s.install.publish(serverID, "ERROR: "+msg)
				return fmt.Errorf("%s", msg)
			}
			env["STEAM_USER"] = user
		}
	}

	s.install.publish(serverID, "Pulling install image "+image+" ...")
	if err := s.docker.PullImage(ctx, image, w); err != nil {
		s.install.publish(serverID, "WARN: image pull: "+err.Error())
	}

	err = s.docker.RunEphemeralOpts(ctx, docker.EphemeralOptions{
		Image:       image,
		DataDir:     dataDir,
		Env:         envSlice(env),
		Script:      script,
		ExtraMounts: extraMounts,
	}, w)
	if err != nil {
		s.db.ExecContext(ctx, "UPDATE servers SET install_status='error' WHERE id=?", serverID)
		s.install.publish(serverID, "=== Install FAILED: "+err.Error()+" ===")
		return err
	}

	s.db.ExecContext(ctx, "UPDATE servers SET installed=1, install_status='done' WHERE id=?", serverID)
	s.install.publish(serverID, "=== Install complete ===")
	return nil
}

// installEnv builds the env map for an install run, including derived helpers.
func installEnv(rt *serverRuntime) map[string]string {
	env := map[string]string{}
	for k, v := range rt.env {
		env[k] = v
	}
	return env
}

func envSlice(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, fmt.Sprintf("%s=%s", k, v))
	}
	return out
}

var _ io.Writer = hubWriter{} // compile-time interface assertion
