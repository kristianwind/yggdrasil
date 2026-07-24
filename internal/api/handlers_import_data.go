package api

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/kristianwind/yggdrasil/internal/docker"
	"github.com/kristianwind/yggdrasil/internal/gameskill"
	"github.com/kristianwind/yggdrasil/internal/rbac"
	"github.com/kristianwind/yggdrasil/internal/wpress"
)

// App-data import — bring an EXISTING deployment of an app into a Yggdrasil
// server: upload the app's own data (a site archive, a database dump) and the
// panel runs the rune's declared import steps against the server's data dir and
// database sidecar. This is onboarding, the mirror of migration (panel↔panel).
// Everything runs in one-shot containers streamed to the install log; the
// server is stopped for the import and started after.
//
// Admin-only: an import runs shell in the app's image against the data dir and
// pipes an arbitrary dump into the database — the same trust level as installing
// a rune.

const importUploadLimit = 16 << 30 // 16 GiB of multipart form; big dumps go via LAN (CF caps at 100MB)

// handleImportInfo reports whether a server's rune supports import and the
// inputs it expects, so the UI can render the upload form (or hide the button).
func (s *Server) handleImportInfo(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	rt, err := s.loadRuntime(r.Context(), id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if rt.gs.Import == nil {
		jsonOK(w, map[string]any{"supported": false})
		return
	}
	jsonOK(w, map[string]any{"supported": true, "inputs": rt.gs.Import.Inputs})
}

// handleImportData runs the rune's import against uploaded files. Admin-only.
func (s *Server) handleImportData(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !s.can(w, r, rbac.ServerControl, s.serverTarget(r.Context(), id)) {
		return
	}
	if !isAdmin(r) {
		jsonError(w, "forbidden: importing runs code in the app's image (admin only)", http.StatusForbidden)
		return
	}
	rt, err := s.loadRuntime(r.Context(), id)
	if err != nil {
		jsonError(w, "not found", http.StatusNotFound)
		return
	}
	if rt.gs.Import == nil {
		jsonError(w, "this rune does not support data import", http.StatusBadRequest)
		return
	}
	if s.install.isActive(id) {
		jsonError(w, "an install or import is already running", http.StatusConflict)
		return
	}
	if err := r.ParseMultipartForm(importUploadLimit); err != nil {
		jsonError(w, "upload too large or malformed — for big dumps import over the LAN, not through a proxy", http.StatusBadRequest)
		return
	}

	// Stage every uploaded input to a temp dir the containers can bind-mount. The
	// dir is removed after the import regardless of outcome — dumps hold secrets.
	staging, err := os.MkdirTemp("", "ygg-import-*")
	if err != nil {
		jsonError(w, "staging error", http.StatusInternalServerError)
		return
	}
	inputs := map[string]string{} // key -> staged host path
	for _, in := range rt.gs.Import.Inputs {
		f, hdr, ferr := r.FormFile(in.Key)
		if ferr != nil {
			if in.Optional {
				continue
			}
			os.RemoveAll(staging)
			jsonError(w, "missing required file: "+in.Key, http.StatusBadRequest)
			return
		}
		dst := filepath.Join(staging, in.Key+"-"+safeFilename(hdr.Filename))
		out, oerr := os.Create(dst)
		if oerr != nil {
			f.Close()
			os.RemoveAll(staging)
			jsonError(w, "staging write error", http.StatusInternalServerError)
			return
		}
		if _, cerr := io.Copy(out, f); cerr != nil {
			out.Close()
			f.Close()
			os.RemoveAll(staging)
			jsonError(w, "staging copy error", http.StatusInternalServerError)
			return
		}
		out.Close()
		f.Close()
		inputs[in.Key] = dst
	}

	if len(inputs) == 0 {
		os.RemoveAll(staging)
		jsonError(w, "no files uploaded — pick at least one input", http.StatusBadRequest)
		return
	}
	s.auditLog(r, "server.import_data", "server:"+id, map[string]any{"inputs": len(inputs)})
	go s.runDataImport(id, rt, inputs, staging) //nolint:errcheck // progress streams to the install log
	w.WriteHeader(http.StatusAccepted)
	jsonOK(w, map[string]string{"status": "importing"})
}

// runDataImport executes the rune's import steps in the background, streaming to
// the install-log hub (the UI already tails it). Stops the server first, starts
// it after — a data import must not race a running app.
func (s *Server) runDataImport(id string, rt *serverRuntime, inputs map[string]string, staging string) error {
	defer recoverLog("runDataImport")
	defer os.RemoveAll(staging)
	if s.install.isActive(id) {
		return fmt.Errorf("busy")
	}
	s.install.setActive(id, true)
	defer s.install.setActive(id, false)
	ctx := context.Background()

	pub := func(line string) { s.install.publish(id, line) }
	pub(fmt.Sprintf("=== Data import started %s ===", time.Now().UTC().Format(time.RFC3339)))

	var dataDir string
	s.db.QueryRowContext(ctx, "SELECT COALESCE(data_dir,'') FROM servers WHERE id=?", id).Scan(&dataDir) //nolint:errcheck
	if dataDir == "" {
		pub("ERROR: server has no data directory")
		return fmt.Errorf("no data dir")
	}

	// Stop the app so nothing writes underneath the import.
	var containerID string
	s.db.QueryRowContext(ctx, "SELECT COALESCE(container_id,'') FROM servers WHERE id=?", id).Scan(&containerID) //nolint:errcheck
	wasRunning := false
	if containerID != "" {
		var status string
		s.db.QueryRowContext(ctx, "SELECT status FROM servers WHERE id=?", id).Scan(&status) //nolint:errcheck
		wasRunning = status == "running"
		pub("Stopping the server for a clean import ...")
		s.gracefulStop(ctx, containerID, rt.gs) //nolint:errcheck
	}

	// A db_import needs the database sidecar reachable — bring the stack up
	// (network + sidecars) without the main app. No-op for single-container runes.
	if importNeedsStack(rt.gs) {
		pub("Bringing up the database sidecar ...")
		if err := s.startStack(ctx, id, dataDir, rt.gs, rt.env); err != nil {
			pub("ERROR: could not start the database: " + err.Error())
			return err
		}
	}

	w := hubWriter{hub: s.install, id: id}
	for i, step := range rt.gs.Import.Steps {
		pub(fmt.Sprintf("--- step %d/%d ---", i+1, len(rt.gs.Import.Steps)))
		if err := s.runImportStep(ctx, id, dataDir, rt, step, inputs, staging, w); err != nil {
			pub("=== Import FAILED: " + err.Error() + " ===")
			return err
		}
	}

	// Reclaim ownership: import containers write as their image's user, but the
	// server (and file manager) run as the panel user. Mirror the install chown.
	chown := fmt.Sprintf("chown -R %d:%d /data 2>/dev/null || true", os.Getuid(), os.Getgid())
	s.docker.RunEphemeralOpts(ctx, docker.EphemeralOptions{
		Image: importChownImage(rt.gs), DataDir: dataDir, Script: chown, User: "0:0",
	}, w) //nolint:errcheck

	pub("=== Data import complete ===")
	if wasRunning {
		pub("Restarting the server ...")
		if err := s.recreateAndStart(ctx, id); err != nil {
			pub("WARN: could not restart after import: " + err.Error())
		}
	}
	go s.notifyServer(id, fmt.Sprintf("📥 Data import finished for %s", s.serverName(id)))
	return nil
}

// runImportStep dispatches one step. Exactly one verb is set (validated at parse).
func (s *Server) runImportStep(ctx context.Context, id, dataDir string, rt *serverRuntime, step gameskill.ImportStep, inputs map[string]string, staging string, w hubWriter) error {
	switch {
	case step.Unpack != "":
		src, ok := inputs[step.Unpack]
		if !ok {
			return nil // optional input not supplied — skip
		}
		to := strings.Trim(step.To, "/")
		if to == "" {
			to = "."
		}
		// Unpack inside a minimal image with the archive mounted read-only. tar
		// handles .tar/.tar.gz/.tgz; unzip handles .zip. Destination is jailed to
		// the data dir by the /data bind.
		script := fmt.Sprintf(`set -e
mkdir -p "/data/%s" && cd "/data/%s"
f=/input/archive
case "$f" in
  *.zip) (command -v unzip >/dev/null && unzip -o "$f") || (apk add --no-cache unzip >/dev/null 2>&1 && unzip -o "$f") ;;
  *) tar -xf "$f" ;;
esac
echo "unpacked into %s"`, to, to, to)
		return s.docker.RunEphemeralOpts(ctx, docker.EphemeralOptions{
			Image:       importUnpackImage,
			DataDir:     dataDir,
			ExtraMounts: map[string]string{src: "/input/archive"},
			Script:      script,
			User:        "0:0",
		}, w)

	case step.DBImport != nil:
		d := step.DBImport
		src, ok := inputs[d.Input]
		if !ok {
			return nil // optional dump not supplied
		}
		cmd := gameskill.ApplyTemplate(d.Command, rt.env)
		// Pipe the dump into the client, decompressing .gz on the fly. The client
		// joins the stack network so it reaches the sidecar by service name.
		script := fmt.Sprintf(`set -e
if echo /input/dump | grep -q '\.gz$' || gzip -t /input/dump 2>/dev/null; then
  gzip -dc /input/dump | %s
else
  %s < /input/dump
fi
echo "database import done"`, cmd, cmd)
		return s.docker.RunEphemeralOpts(ctx, docker.EphemeralOptions{
			Image:        gameskill.ApplyTemplate(d.Image, rt.env),
			ExtraMounts:  map[string]string{src: "/input/dump"},
			Env:          envSlice(rt.env),
			Script:       script,
			Network:      stackNetworkName(id),
			NetworkAlias: "ygg-import",
		}, w)

	case step.Wpress != nil:
		src, ok := inputs[step.Wpress.Input]
		if !ok {
			return nil // optional archive not supplied — skip
		}
		return s.runWpressStep(ctx, dataDir, step.Wpress, src, inputs, staging, w)

	case step.Script != "":
		// Run the app's own image against the data dir. Input paths are exposed as
		// $YGG_INPUT_<KEY> so a script can reference an upload if it needs to.
		env := map[string]string{}
		for k, v := range rt.env {
			env[k] = v
		}
		mounts := map[string]string{}
		for key, path := range inputs {
			mp := "/input/" + key
			mounts[path] = mp
			env["YGG_INPUT_"+strings.ToUpper(key)] = mp
		}
		return s.docker.RunEphemeralOpts(ctx, docker.EphemeralOptions{
			Image:       gameskill.ApplyTemplate(rt.gs.Docker.Image, rt.env),
			DataDir:     dataDir,
			ExtraMounts: mounts,
			Env:         envSlice(env),
			Script:      gameskill.ApplyTemplate(step.Script, rt.env),
			User:        "0:0",
		}, w)
	}
	return nil
}

// runWpressStep extracts an All-in-One WP Migration archive natively (the
// format is trivial framing, and Go beats shell at binary headers): wp-content
// files land under `to`, and database.sql — table prefix unmasked to wp_ —
// becomes the virtual input db_key for the following db_import step. The site's
// URLs are deliberately NOT rewritten: naive replaces corrupt PHP-serialized
// data, and the panel can't know the final domain anyway.
func (s *Server) runWpressStep(ctx context.Context, dataDir string, st *gameskill.Wpress, src string, inputs map[string]string, staging string, w hubWriter) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	to := strings.Trim(st.To, "/")
	if to == "" {
		to = "wp-content"
	}
	root := filepath.Join(dataDir, filepath.Clean("/"+to))
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	r := wpress.NewReader(f)
	files, bytes := 0, int64(0)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		e, rerr := r.Next()
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fmt.Errorf("wpress parse: %w", rerr)
		}
		switch e.Path {
		case "database.sql":
			if st.DBKey == "" {
				continue // rune chose not to load the DB — drain and move on
			}
			dumpPath := filepath.Join(staging, "wpress-database.sql")
			out, oerr := os.Create(dumpPath)
			if oerr != nil {
				return oerr
			}
			// The dump masks the table prefix; the panel's generated wp-config
			// uses WordPress's default wp_.
			if _, cerr := io.Copy(out, wpress.PrefixReplacer(e.Body, "wp_")); cerr != nil {
				out.Close()
				return cerr
			}
			out.Close()
			inputs[st.DBKey] = dumpPath
			fmt.Fprintf(w, "extracted database.sql (%d MB, table prefix → wp_)\n", e.Size>>20)
		case "package.json", "multisite.json":
			io.Copy(io.Discard, e.Body) //nolint:errcheck // metadata — not site files
		default:
			dest := filepath.Join(root, filepath.Clean("/"+e.Path))
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return err
			}
			out, oerr := os.Create(dest)
			if oerr != nil {
				return oerr
			}
			if _, cerr := io.Copy(out, e.Body); cerr != nil {
				out.Close()
				return cerr
			}
			out.Close()
			files++
			bytes += e.Size
			if files%2000 == 0 {
				fmt.Fprintf(w, "… %d files, %d MB\n", files, bytes>>20)
			}
		}
	}
	fmt.Fprintf(w, "wpress: %d files (%d MB) into %s\n", files, bytes>>20, to)
	return nil
}

// importNeedsStack reports whether any step imports into a database sidecar.
func importNeedsStack(gs *gameskill.Gameskill) bool {
	if gs.Import == nil {
		return false
	}
	for _, st := range gs.Import.Steps {
		if st.DBImport != nil {
			return true
		}
	}
	return false
}

// importChownImage picks a small image guaranteed to exist for the post-import
// ownership fix — the app image if it's already local, else the unpack image.
func importChownImage(gs *gameskill.Gameskill) string {
	if gs.Docker.Image != "" {
		return gs.Docker.Image
	}
	return importUnpackImage
}

// importUnpackImage is a tiny image with tar (and apk to add unzip on demand).
const importUnpackImage = "alpine:3"
