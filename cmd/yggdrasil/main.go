// Command yggdrasil is the Yggdrasil control plane: a single static binary that
// serves the web UI + REST/WebSocket API and drives game servers via Docker.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	yggdrasil "github.com/kristianwind/yggdrasil"
	"github.com/kristianwind/yggdrasil/internal/api"
	"github.com/kristianwind/yggdrasil/internal/auth"
	"github.com/kristianwind/yggdrasil/internal/config"
	"github.com/kristianwind/yggdrasil/internal/db"
	"github.com/kristianwind/yggdrasil/internal/docker"
	"github.com/kristianwind/yggdrasil/internal/gameskill"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("yggdrasil: ")

	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "--version", "-v":
			fmt.Printf("yggdrasil %s\n", version)
			return
		case "gen-config":
			path := "config.yaml"
			if len(os.Args) > 2 {
				path = os.Args[2]
			}
			if err := config.WriteExample(path); err != nil {
				log.Fatalf("gen-config: %v", err)
			}
			fmt.Printf("wrote example config to %s\n", path)
			return
		case "update":
			fmt.Println("self-update is implemented in a later phase; re-run install.sh to upgrade")
			return
		}
	}

	cfgPath := flag.String("config", "/etc/yggdrasil/config.yaml", "path to config.yaml")
	flag.Parse()

	if err := run(*cfgPath); err != nil {
		log.Fatal(err)
	}
}

func run(cfgPath string) error {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Ensure a persistent auth secret exists; generate + persist on first boot.
	if cfg.Auth.SecretKey == "" {
		key, err := auth.GenerateSecureKey(32)
		if err != nil {
			return fmt.Errorf("generate secret: %w", err)
		}
		cfg.Auth.SecretKey = key
		if err := persistSecret(cfgPath, key); err != nil {
			log.Printf("warning: could not persist secret key (%v); sessions reset on restart", err)
		}
	}

	if err := os.MkdirAll(filepath.Dir(cfg.Database.Path), 0755); err != nil {
		return fmt.Errorf("create data dir: %w", err)
	}
	database, err := db.Open(cfg.Database.Path)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer database.Close()

	// First admin (from config). If no password given, generate and log one once.
	if cfg.Admin.Username != "" {
		pw := cfg.Admin.Password
		if pw == "" {
			pw, _ = auth.GenerateSecureKey(12)
			log.Printf("generated admin password for %q: %s", cfg.Admin.Username, pw)
		}
		if err := auth.EnsureAdmin(database, cfg.Admin.Username, pw); err != nil {
			return fmt.Errorf("ensure admin: %w", err)
		}
	}

	if err := loadBuiltinGameskills(database); err != nil {
		log.Printf("warning: loading builtin gameskills: %v", err)
	}

	dc, err := docker.New(cfg.Docker.Socket)
	if err != nil {
		return fmt.Errorf("docker client: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	if err := dc.Ping(pingCtx); err != nil {
		log.Printf("warning: Docker daemon not reachable (%v); start it to manage servers", err)
	}
	cancel()

	srv := api.New(cfg, database, dc, yggdrasil.WebFS)
	srv.SetVersion(version)

	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("shutting down...")
		shutCtx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		_ = httpSrv.Shutdown(shutCtx)
	}()

	log.Printf("Yggdrasil %s listening on http://%s", version, addr)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// loadBuiltinGameskills upserts the embedded gameskill YAML files as builtins.
// Re-runnable: existing rows are updated, preserving any server references.
func loadBuiltinGameskills(database *sql.DB) error {
	entries, err := fs.ReadDir(yggdrasil.GameskillsFS, "gameskills")
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		data, err := fs.ReadFile(yggdrasil.GameskillsFS, "gameskills/"+e.Name())
		if err != nil {
			log.Printf("read %s: %v", e.Name(), err)
			continue
		}
		gs, err := gameskill.Parse(data)
		if err != nil {
			log.Printf("invalid builtin gameskill %s: %v", e.Name(), err)
			continue
		}
		_, err = database.Exec(`
			INSERT INTO gameskills (id, name, category, version, yaml_blob, builtin)
			VALUES (?,?,?,?,?,1)
			ON CONFLICT(id) DO UPDATE SET
				name=excluded.name, category=excluded.category,
				version=excluded.version, yaml_blob=excluded.yaml_blob, builtin=1
		`, gs.ID, gs.Name, gs.Category, gs.Version, string(data))
		if err != nil {
			log.Printf("upsert gameskill %s: %v", gs.ID, err)
		}
	}
	return nil
}

// persistSecret appends a generated secret key to the config file if absent.
func persistSecret(cfgPath, key string) error {
	data, err := os.ReadFile(cfgPath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if strings.Contains(string(data), "secret_key") {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		return err
	}
	f, err := os.OpenFile(cfgPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = fmt.Fprintf(f, "\nauth:\n  secret_key: %q\n", key)
	return err
}
