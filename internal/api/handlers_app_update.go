package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/kristianwind/yggdrasil/internal/docker"
	"github.com/kristianwind/yggdrasil/internal/gameskill"
	"github.com/kristianwind/yggdrasil/internal/rbac"
)

// App update — run a rune's declared `update:` script against an installation
// that already exists. See gameskill.Update for why re-running install cannot do
// this: an app image only populates an EMPTY data dir, and a rune that hardens
// its data against the app (WordPress core kept read-only to PHP) closes the
// app's own updater at the same time. Without this the only way to patch such a
// site is a hand-written `docker run` on the host.
//
// Same trust level as install/import — the script is shell from the rune, run as
// root against the server's data — so it is admin-only and audited. It reuses the
// install log: one place to watch, and the isActive flag already serialises
// install, import and update against each other.

// handleAppUpdateInfo reports whether this server's rune declares an update, and
// the button text to use, so the UI can show it only where it exists.
func (s *Server) handleAppUpdateInfo(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rt, err := s.loadRuntime(r.Context(), id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if rt.gs.Update == nil {
		jsonOK(w, map[string]any{"supported": false})
		return
	}
	label := rt.gs.Update.Label
	if label == "" {
		label = "Update app"
	}
	jsonOK(w, map[string]any{"supported": true, "label": label})
}

// handleAppUpdate kicks the update off in the background; progress streams to the
// install-log WebSocket the UI is already tailing.
func (s *Server) handleAppUpdate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerControl, s.serverTarget(r.Context(), id)) {
		return
	}
	if !isAdmin(r) {
		jsonError(w, "forbidden: an update runs the rune's script as root in the app's image (admin only)", http.StatusForbidden)
		return
	}
	rt, err := s.loadRuntime(r.Context(), id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if rt.gs.Update == nil {
		jsonError(w, "this rune does not declare an update", http.StatusBadRequest)
		return
	}
	if s.install.isActive(id) {
		jsonError(w, "an install, import or update is already running", http.StatusConflict)
		return
	}
	s.auditLog(r, "server.app_update", "server:"+id, nil)
	go s.runAppUpdate(id, rt) //nolint:errcheck // progress streams to the install log
	w.WriteHeader(http.StatusAccepted)
	jsonOK(w, map[string]string{"status": "updating"})
}

// runAppUpdate executes the rune's update script. The app is stopped first so it
// cannot write underneath the update (PHP rewriting core mid-swap is exactly the
// corruption this avoids) and restarted after if it was running — the same
// orchestration a data import uses.
func (s *Server) runAppUpdate(id string, rt *serverRuntime) error {
	defer recoverLog("runAppUpdate")
	if s.install.isActive(id) {
		return fmt.Errorf("busy")
	}
	s.install.setActive(id, true)
	defer s.install.setActive(id, false)

	ctx := context.Background()
	pub := func(line string) { s.install.publish(id, line) }
	w := hubWriter{hub: s.install, id: id}

	var dataDir, containerID, status string
	if err := s.db.QueryRowContext(ctx,
		"SELECT COALESCE(data_dir,''), COALESCE(container_id,''), COALESCE(status,'') FROM servers WHERE id=?", id,
	).Scan(&dataDir, &containerID, &status); err != nil {
		return err
	}
	if dataDir == "" {
		pub("ERROR: server has no data directory")
		return fmt.Errorf("no data dir")
	}

	pub(fmt.Sprintf("=== Update started %s ===", time.Now().UTC().Format(time.RFC3339)))

	// "starting" counts as running: the container is up and writing (a WordPress
	// stack is serving requests well before its done_regex matches), and that is
	// exactly what must not happen underneath a core update.
	wasRunning := status == "running" || status == "starting"
	if containerID != "" && wasRunning {
		pub("Stopping the app so nothing writes underneath the update ...")
		s.gracefulStop(ctx, containerID, rt.gs) //nolint:errcheck
	}

	// Stopping a server takes its sidecars down with it, so an update on a stopped
	// stack app would have no database to talk to. Bring them back if they are not
	// already up; leave running ones alone (recreating a database mid-update is
	// precisely what we don't want).
	if len(rt.gs.Services) > 0 {
		if err := s.ensureStackUp(ctx, id, dataDir, rt, pub); err != nil {
			pub("=== Update FAILED: " + err.Error() + " ===")
			return err
		}
	}

	image := gameskill.ApplyTemplate(rt.gs.Update.Image, rt.env)
	if image == "" {
		image = gameskill.ApplyTemplate(rt.gs.Docker.Image, rt.env)
	}
	script := gameskill.ApplyTemplate(rt.gs.Update.Script, rt.env)

	pub("Pulling update image " + image + " ...")
	if err := s.docker.PullImage(ctx, image, w); err != nil {
		pub("WARN: image pull: " + err.Error())
	}

	// Only join the stack network when there is one — a single-container rune has
	// no such network, and naming a missing one fails the run outright.
	network, alias := "", ""
	if len(rt.gs.Services) > 0 {
		network, alias = stackNetworkName(id), "ygg-update"
	}
	err := s.docker.RunEphemeralOpts(ctx, docker.EphemeralOptions{
		Image:        image,
		DataDir:      dataDir,
		Env:          appUpdateEnv(rt),
		Script:       script,
		User:         "0:0", // root: writes through a mode it does not own, so the rune's hardening survives
		Network:      network,
		NetworkAlias: alias,
	}, w)
	if err != nil {
		pub("=== Update FAILED: " + err.Error() + " ===")
		if wasRunning {
			pub("Restarting the app ...")
			if rerr := s.recreateAndStart(ctx, id); rerr != nil {
				pub("WARN: could not restart after the failed update: " + rerr.Error())
			}
		}
		return err
	}

	// Reclaim ownership of anything the update ADDED, so the Files tab and backups
	// keep working. Measured on a real WordPress update: existing files keep their
	// owner (root writes through a mode it does not own) and only genuinely new
	// files — four translation files, there — arrive owned by root.
	//
	// Two deliberate narrowings, both load-bearing:
	//   - user only, not install's `<uid>:<gid>`. The GROUP is how a rune grants the
	//     app access to its own data (WordPress gives www-data group write while the
	//     panel stays owner); rewriting it would revoke PHP's access to wp-content.
	//   - never `.stack`, where an app-stack rune's sidecars keep their data. That
	//     belongs to the database's own uid (mysql, postgres), and handing it to the
	//     panel user stops the database starting.
	chown := fmt.Sprintf(
		"find /data -mindepth 1 -maxdepth 1 ! -name .stack -exec chown -R %d {} + 2>/dev/null || true",
		os.Getuid())
	if cerr := s.docker.RunEphemeralOpts(ctx, docker.EphemeralOptions{
		Image: image, DataDir: dataDir, Script: chown, User: "0:0",
	}, w); cerr != nil {
		pub("WARN: could not set file ownership: " + cerr.Error())
	}

	pub("=== Update complete ===")
	if wasRunning {
		pub("Restarting the app ...")
		if rerr := s.recreateAndStart(ctx, id); rerr != nil {
			pub("WARN: could not restart after the update: " + rerr.Error())
		}
	}
	go s.notifyServer(id, fmt.Sprintf("⬆️ Update finished for %s", s.serverName(id)))
	return nil
}

// appUpdateEnv gives the update container the environment the APP runs with, not
// just the server's variables — the rune's fixed docker.env too, templated the
// same way and overridable by a user variable of the same key.
//
// Found by running the WordPress update against a real site: the image writes a
// wp-config.php that reads its credentials from the environment
// (`getenv_docker('WORDPRESS_DB_USER', …)`), so a container without WORDPRESS_DB_*
// falls back to the image's placeholder defaults and dies with "Error establishing
// a database connection". An update runs the app's own tooling against the app's
// own data; it needs to see what the app sees.
func appUpdateEnv(rt *serverRuntime) []string {
	env := map[string]string{}
	for k, v := range rt.gs.Docker.Env {
		env[k] = gameskill.ApplyTemplate(v, rt.env)
	}
	for k, v := range rt.env {
		env[k] = v
	}
	return envSlice(env)
}

// ensureStackUp brings a server's sidecars back if they are not running, and
// waits for the containers to actually be up before the update script talks to
// them. A sidecar on an existing data dir accepts connections within seconds; an
// update script that queries a database should still expect to retry (the
// WordPress rune polls before it starts).
func (s *Server) ensureStackUp(ctx context.Context, id, dataDir string, rt *serverRuntime, pub func(string)) error {
	allUp := true
	for _, svc := range rt.gs.Services {
		running, _, err := s.docker.State(ctx, sidecarName(id, svc.Name))
		if err != nil || !running {
			allUp = false
			break
		}
	}
	if allUp {
		return nil
	}
	pub("Bringing the app's own services up for the update ...")
	if err := s.startStack(ctx, id, dataDir, rt.gs, rt.env); err != nil {
		return fmt.Errorf("could not start the app's services: %w", err)
	}
	for i := 0; i < 30; i++ {
		allUp = true
		for _, svc := range rt.gs.Services {
			running, _, err := s.docker.State(ctx, sidecarName(id, svc.Name))
			if err != nil || !running {
				allUp = false
				break
			}
		}
		if allUp {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("the app's services did not come up")
}
