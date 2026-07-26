package main

import (
	"flag"
	"fmt"
	"strings"

	"github.com/kristianwind/yggdrasil/internal/auth"
	"github.com/kristianwind/yggdrasil/internal/config"
	"github.com/kristianwind/yggdrasil/internal/db"
)

// runResetPassword handles `yggdrasil reset-password <username> [--password pw]`:
// the break-glass path to regain access when no email/SMTP is configured (or an
// admin has locked themselves out). It runs directly against the database on the
// host, so it needs no running panel and no auth beyond shell access to the box.
// A generated password is printed once; an explicit one must clear the same
// 12-char floor the panel enforces. It bumps token_version to revoke any session
// the old password still held.
func runResetPassword(args []string) error {
	fs := flag.NewFlagSet("reset-password", flag.ExitOnError)
	cfgPath := fs.String("config", "/etc/yggdrasil/config.yaml", "path to config.yaml")
	pw := fs.String("password", "", "new password (min 12 chars); generated and printed if omitted")

	// Go's flag package stops at the first non-flag token, so `reset-password
	// <user> --config X` would silently ignore the flag. Accept the username
	// either before or after the flags by pulling a leading positional out first.
	username := ""
	rest := args
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		username = args[0]
		rest = args[1:]
	}
	fs.Parse(rest)
	if username == "" {
		if fs.NArg() < 1 {
			return fmt.Errorf("usage: yggdrasil reset-password <username> [--password <pw>]")
		}
		username = fs.Arg(0)
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	database, err := db.Open(cfg.Database.Path)
	if err != nil {
		return fmt.Errorf("open db: %w", err)
	}
	defer database.Close()

	var id string
	if err := database.QueryRow("SELECT id FROM users WHERE username=?", username).Scan(&id); err != nil {
		return fmt.Errorf("no such user %q", username)
	}

	newPw := *pw
	generated := false
	if newPw == "" {
		// 12 random bytes → 16 URL-safe chars, comfortably past the 12-char floor.
		newPw, err = auth.GenerateSecureKey(12)
		if err != nil {
			return err
		}
		generated = true
	} else if len([]rune(newPw)) < 12 {
		return fmt.Errorf("password must be at least 12 characters")
	}

	hash, err := auth.HashPassword(newPw)
	if err != nil {
		return err
	}
	if _, err := database.Exec(
		"UPDATE users SET password_hash=?, token_version = COALESCE(token_version,0)+1 WHERE id=?",
		hash, id); err != nil {
		return fmt.Errorf("update password: %w", err)
	}

	if generated {
		fmt.Printf("\n=== Password reset for %q (shown once) ===\n"+
			"  new password: %s\n"+
			"  Log in and change it now.\n"+
			"===============================================\n\n", username, newPw)
	} else {
		fmt.Printf("Password for %q has been reset.\n", username)
	}
	return nil
}
