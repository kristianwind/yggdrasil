package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/kristianwind/yggdrasil/internal/config"
	"github.com/kristianwind/yggdrasil/internal/db"
	"github.com/kristianwind/yggdrasil/internal/migrate"
)

// runMigrate handles `yggdrasil migrate export|import` — moving a whole panel to
// another host as one bundle (database + every data dir + the secret_key).
func runMigrate(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: yggdrasil migrate export|import …")
	}
	switch args[0] {
	case "export":
		return migrateExport(args[1:])
	case "import":
		return migrateImport(args[1:])
	default:
		return fmt.Errorf("unknown migrate command %q (want export|import)", args[0])
	}
}

func migrateExport(args []string) error {
	fs := flag.NewFlagSet("migrate export", flag.ExitOnError)
	cfgPath := fs.String("config", "/etc/yggdrasil/config.yaml", "path to config.yaml")
	out := fs.String("o", "", "bundle output path (default: stdout)")
	fs.Parse(args)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	database, err := db.Open(cfg.Database.Path)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer database.Close()

	w := os.Stdout
	if *out != "" {
		f, err := os.Create(*out)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	if err := migrate.Export(cfg, database, w); err != nil {
		return err
	}
	if *out != "" {
		fmt.Fprintf(os.Stderr, "Wrote migration bundle to %s\n", *out)
		fmt.Fprintln(os.Stderr, "⚠️  This bundle contains the panel's secret_key, password hashes and every")
		fmt.Fprintln(os.Stderr, "    encrypted secret. Treat it like a private key — transfer it securely.")
	}
	return nil
}

func migrateImport(args []string) error {
	fs := flag.NewFlagSet("migrate import", flag.ExitOnError)
	cfgPath := fs.String("config", "/etc/yggdrasil/config.yaml", "path to config.yaml")
	fs.Parse(args)
	if fs.NArg() < 1 {
		return fmt.Errorf("usage: yggdrasil migrate import <bundle.tar.gz>")
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w (install and gen-config on the new host first)", err)
	}
	f, err := os.Open(fs.Arg(0))
	if err != nil {
		return err
	}
	defer f.Close()

	man, err := migrate.Import(f, cfg.Database.Path)
	if err != nil {
		return err
	}
	fmt.Printf("Imported %d server(s) and the database.\n", len(man.Servers))
	if man.SecretKey != cfg.Auth.SecretKey {
		fmt.Println()
		fmt.Println("╭─ ACTION REQUIRED ─────────────────────────────────────────────────╮")
		fmt.Println("│ The imported panel's encrypted secrets (RCON passwords, API keys)  │")
		fmt.Println("│ can only be decrypted with the ORIGINAL secret_key. Set this in    │")
		fmt.Printf("│ %s before starting:\n", *cfgPath)
		fmt.Println("│                                                                    │")
		fmt.Printf("│   auth:\n│     secret_key: %q\n", man.SecretKey)
		fmt.Println("╰────────────────────────────────────────────────────────────────────╯")
	} else {
		fmt.Println("secret_key already matches — you're ready to start the panel.")
	}
	return nil
}
